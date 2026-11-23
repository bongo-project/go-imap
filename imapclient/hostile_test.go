package imapclient_test

import (
	"bufio"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// fakeFetchServer connects a client to a scripted server. After the greeting
// and the first command, it calls respond with the command tag and the
// connection; respond writes the untagged data. The server then closes.
func fakeFetchServer(t *testing.T, options *imapclient.Options, respond func(tag string, w io.Writer)) *imapclient.Client {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { serverConn.Close() })
	go func() {
		if _, err := io.WriteString(serverConn, "* PREAUTH [CAPABILITY IMAP4rev1] ready\r\n"); err != nil {
			return
		}
		line, err := bufio.NewReader(serverConn).ReadString('\n')
		if err != nil {
			return
		}
		tag, _, _ := strings.Cut(line, " ")
		respond(tag, serverConn)
	}()

	client := imapclient.New(clientConn, options)
	t.Cleanup(func() { client.Close() })
	return client
}

func fetchBodyStructure(client *imapclient.Client) error {
	_, err := client.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{BodyStructure: &imap.FetchItemBodyStructure{}}).Collect()
	return err
}

func fetchEnvelope(client *imapclient.Client) ([]*imapclient.FetchMessageBuffer, error) {
	return client.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Envelope: true}).Collect()
}

const hostileChildEnv = "GOIMAP_TEST_HOSTILE_CHILD"

// TestFetchBodyStructureNesting sends a deeply nested BODYSTRUCTURE. The
// scenario runs in a child process because the failure mode of an unbounded
// recursion is a fatal stack overflow that cannot be recovered.
func TestFetchBodyStructureNesting(t *testing.T) {
	if os.Getenv(hostileChildEnv) == "1" {
		client := fakeFetchServer(t, nil, func(tag string, w io.Writer) {
			io.WriteString(w, "* 1 FETCH (BODYSTRUCTURE ")
			chunk := []byte(strings.Repeat("(", 1<<16))
			for i := 0; i < 256; i++ { // 16 MiB
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		})
		done := make(chan error, 1)
		go func() { done <- fetchBodyStructure(client) }()
		select {
		case err := <-done:
			if err == nil {
				os.Exit(3)
			}
			if !strings.Contains(err.Error(), "nested too deeply") {
				os.Stderr.WriteString("unexpected error: " + err.Error() + "\n")
				os.Exit(4)
			}
			os.Exit(0)
		case <-time.After(30 * time.Second):
			os.Exit(5)
		}
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestFetchBodyStructureNesting$")
	cmd.Env = append(os.Environ(), hostileChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := string(out)
		if len(s) > 2000 {
			s = s[:2000]
		}
		t.Fatalf("child process failed: %v\n%v", err, s)
	}
}

func TestFetchBodyStructureNestingBelowLimit(t *testing.T) {
	const depth = 50
	body := strings.Repeat("(", depth) +
		`("TEXT" "PLAIN" ("CHARSET" "utf-8") NIL NIL "7BIT" 5 1)` +
		strings.Repeat(` "MIXED")`, depth)

	client := fakeFetchServer(t, nil, func(tag string, w io.Writer) {
		io.WriteString(w, "* 1 FETCH (BODYSTRUCTURE "+body+")\r\n")
		io.WriteString(w, tag+" OK done\r\n")
	})
	msgs, err := client.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{BodyStructure: &imap.FetchItemBodyStructure{}}).Collect()
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	var walked int
	msgs[0].BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
		walked++
		return true
	})
	if walked != depth+1 {
		t.Errorf("walked %v parts, want %v", walked, depth+1)
	}
}

func TestFetchEnvelopeOversizedLiteral(t *testing.T) {
	const litSize = 64 << 20

	client := fakeFetchServer(t, nil, func(tag string, w io.Writer) {
		io.WriteString(w, `* 1 FETCH (ENVELOPE (NIL {67108864}`+"\r\n")
		chunk := make([]byte, 1<<16)
		for i := 0; i < litSize/len(chunk); i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := fetchEnvelope(client)
	runtime.ReadMemStats(&after)

	if err == nil {
		t.Fatalf("Fetch() = nil, want an error for an oversized literal")
	}
	if !strings.Contains(err.Error(), "literal") {
		t.Errorf("Fetch() = %v, want an error mentioning the literal", err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > litSize/8 {
		t.Errorf("allocated %v bytes for a refused %v byte literal", alloc, litSize)
	}
}

func TestFetchEnvelopeLiteralLimitOption(t *testing.T) {
	subject := strings.Repeat("s", 100)
	respond := func(tag string, w io.Writer) {
		io.WriteString(w, `* 1 FETCH (ENVELOPE (NIL {100}`+"\r\n"+subject+` NIL NIL NIL NIL NIL NIL NIL NIL))`+"\r\n")
		io.WriteString(w, tag+" OK done\r\n")
	}

	client := fakeFetchServer(t, &imapclient.Options{MaxBufferedLiteralSize: 50}, respond)
	if _, err := fetchEnvelope(client); err == nil {
		t.Errorf("Fetch() = nil, want an error with a 50 byte limit")
	}

	client = fakeFetchServer(t, &imapclient.Options{MaxBufferedLiteralSize: 200}, respond)
	msgs, err := fetchEnvelope(client)
	if err != nil {
		t.Fatalf("Fetch() = %v with a 200 byte limit", err)
	}
	if got := msgs[0].Envelope.Subject; got != subject {
		t.Errorf("Subject = %q, want %q", got, subject)
	}
}

func TestFetchEnvelopeModestLiterals(t *testing.T) {
	client := fakeFetchServer(t, nil, func(tag string, w io.Writer) {
		io.WriteString(w, `* 1 FETCH (ENVELOPE (NIL {5}`+"\r\n"+`hello `+
			`(({3}`+"\r\n"+`Bob NIL "bob" "example.org")) NIL NIL NIL NIL NIL NIL "<id@example.org>"))`+"\r\n")
		io.WriteString(w, tag+" OK done\r\n")
	})
	msgs, err := fetchEnvelope(client)
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	env := msgs[0].Envelope
	if env.Subject != "hello" || len(env.From) != 1 || env.From[0].Name != "Bob" || env.MessageID != "id@example.org" {
		t.Errorf("unexpected envelope: %+v", env)
	}
}

func TestFetchBodySectionLiteralStreams(t *testing.T) {
	const size = 1 << 20 // well above the default buffered literal limit
	client := fakeFetchServer(t, nil, func(tag string, w io.Writer) {
		io.WriteString(w, "* 1 FETCH (BODY[] {1048576}\r\n")
		io.WriteString(w, strings.Repeat("x", size))
		io.WriteString(w, ")\r\n"+tag+" OK done\r\n")
	})
	msgs, err := client.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{{}},
	}).Collect()
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if got := len(msgs[0].BodySection[0].Bytes); got != size {
		t.Errorf("body section is %v bytes, want %v", got, size)
	}
}
