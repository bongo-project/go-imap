package imapclient_test

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// TestACL_mailboxNameForms sends every ACL command with the mailbox name as a
// quoted string containing a space and as a synchronizing literal. Names are
// checked through their effects: a mis-decoded name would address a folder
// that does not exist and be answered with NO.
func TestACL_mailboxNameForms(t *testing.T) {
	for _, form := range []string{"quoted", "literal"} {
		t.Run(form, func(t *testing.T) {
			memServer := imapmemserver.New()
			user := imapmemserver.NewUser(testUsername, testPassword)
			user.Create("INBOX", nil)
			user.Create("Shared Folder", nil)
			memServer.AddUser(user)

			conn, server := newMemServerConn(t, memServer.NewSession)
			defer server.Close()
			defer conn.Close()

			br := bufio.NewReader(conn)
			readLine := func() string {
				t.Helper()
				line, err := br.ReadString('\n')
				if err != nil {
					t.Fatalf("ReadString() = %v", err)
				}
				return strings.TrimRight(line, "\r\n")
			}
			write := func(s string) {
				t.Helper()
				if _, err := io.WriteString(conn, s); err != nil {
					t.Fatalf("WriteString() = %v", err)
				}
			}
			readResponse := func(tag string) []string {
				t.Helper()
				var lines []string
				for {
					line := readLine()
					lines = append(lines, line)
					if strings.HasPrefix(line, tag+" ") {
						return lines
					}
				}
			}
			const mbox = "Shared Folder"
			n := 1
			// run sends "<cmd> <mailbox><suffix>" and returns the response.
			run := func(cmd, suffix string) []string {
				t.Helper()
				n++
				tag := fmt.Sprintf("A%d", n)
				if form == "quoted" {
					write(fmt.Sprintf("%v %v %q%v\r\n", tag, cmd, mbox, suffix))
				} else {
					write(fmt.Sprintf("%v %v {%d}\r\n", tag, cmd, len(mbox)))
					if line := readLine(); !strings.HasPrefix(line, "+ ") {
						t.Fatalf("got %q, want continuation request", line)
					}
					write(mbox + suffix + "\r\n")
				}
				return readResponse(tag)
			}
			wantOK := func(lines []string, cmd string) {
				t.Helper()
				if last := lines[len(lines)-1]; !strings.Contains(last, " OK ") {
					t.Fatalf("%v response = %q, want OK", cmd, lines)
				}
			}
			hasLine := func(lines []string, want string) bool {
				for _, l := range lines {
					if l == want {
						return true
					}
				}
				return false
			}
			aclLine := func(lines []string) string {
				for _, l := range lines {
					if strings.HasPrefix(l, `* ACL "Shared Folder"`) {
						return l
					}
				}
				return ""
			}

			readLine() // greeting
			write("A1 LOGIN " + testUsername + " " + testPassword + "\r\n")
			wantOK(readResponse("A1"), "LOGIN")

			lines := run("SETACL", " bob lrs")
			wantOK(lines, "SETACL")

			lines = run("GETACL", "")
			wantOK(lines, "GETACL")
			if l := aclLine(lines); !strings.HasSuffix(l, ` "bob" "lrs"`) {
				t.Errorf("GETACL response = %q, want an ACL line ending in bob lrs", lines)
			}

			lines = run("LISTRIGHTS", " bob")
			wantOK(lines, "LISTRIGHTS")
			found := false
			for _, l := range lines {
				if strings.HasPrefix(l, `* LISTRIGHTS "Shared Folder" "bob" "" `) {
					found = true
				}
			}
			if !found {
				t.Errorf("LISTRIGHTS response = %q, want a LISTRIGHTS line for Shared Folder and bob", lines)
			}

			lines = run("MYRIGHTS", "")
			wantOK(lines, "MYRIGHTS")
			wantMyRights := `* MYRIGHTS "Shared Folder" "` + string(imap.RightSetAll) + `"`
			if !hasLine(lines, wantMyRights) {
				t.Errorf("MYRIGHTS response = %q, want line %q", lines, wantMyRights)
			}

			lines = run("DELETEACL", " bob")
			wantOK(lines, "DELETEACL")

			lines = run("GETACL", "")
			wantOK(lines, "GETACL")
			if l := aclLine(lines); l == "" || strings.Contains(l, ` "bob" "lrs"`) {
				t.Errorf("GETACL after DELETEACL = %q, want an ACL line where bob no longer has lrs", lines)
			}

			// The connection is still in sync after the literals
			write("A99 NOOP\r\n")
			wantOK(readResponse("A99"), "NOOP")
		})
	}
}

// TestACL_literalIdentifier sends the identifier as a literal.
func TestACL_literalIdentifier(t *testing.T) {
	memServer := imapmemserver.New()
	user := imapmemserver.NewUser(testUsername, testPassword)
	user.Create("INBOX", nil)
	memServer.AddUser(user)

	conn, server := newMemServerConn(t, memServer.NewSession)
	defer server.Close()
	defer conn.Close()

	br := bufio.NewReader(conn)
	readLine := func() string {
		t.Helper()
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("ReadString() = %v", err)
		}
		return strings.TrimRight(line, "\r\n")
	}
	write := func(s string) {
		t.Helper()
		if _, err := io.WriteString(conn, s); err != nil {
			t.Fatalf("WriteString() = %v", err)
		}
	}
	readResponse := func(tag string) []string {
		t.Helper()
		var lines []string
		for {
			line := readLine()
			lines = append(lines, line)
			if strings.HasPrefix(line, tag+" ") {
				return lines
			}
		}
	}

	readLine()
	write("A1 LOGIN " + testUsername + " " + testPassword + "\r\n")
	readResponse("A1")

	write("A2 SETACL INBOX {3}\r\n")
	if line := readLine(); !strings.HasPrefix(line, "+ ") {
		t.Fatalf("got %q, want continuation request", line)
	}
	write("bob lrs\r\n")
	if got := readResponse("A2"); !strings.HasPrefix(got[len(got)-1], "A2 OK") {
		t.Fatalf("SETACL response = %q, want OK", got)
	}

	write("A3 GETACL INBOX\r\n")
	got := readResponse("A3")
	if !strings.HasPrefix(got[len(got)-1], "A3 OK") || !strings.Contains(strings.Join(got, "\n"), ` "bob" "lrs"`) {
		t.Errorf("GETACL response = %q, want bob lrs", got)
	}

	write("A4 DELETEACL INBOX {3}\r\n")
	if line := readLine(); !strings.HasPrefix(line, "+ ") {
		t.Fatalf("got %q, want continuation request", line)
	}
	write("bob\r\n")
	if got := readResponse("A4"); !strings.HasPrefix(got[len(got)-1], "A4 OK") {
		t.Fatalf("DELETEACL response = %q, want OK", got)
	}

	write("A5 GETACL INBOX\r\n")
	got = readResponse("A5")
	if strings.Contains(strings.Join(got, "\n"), ` "bob" "lrs"`) {
		t.Errorf("GETACL after DELETEACL = %q, want bob without lrs", got)
	}
}

// noACLSession hides the ACL and QUOTA support of the wrapped session.
type noACLSession struct {
	imapserver.SessionIMAP4rev2
}

func TestACL_notSupported(t *testing.T) {
	memServer := imapmemserver.New()
	user := imapmemserver.NewUser(testUsername, testPassword)
	user.Create("INBOX", nil)
	memServer.AddUser(user)

	conn, server := newMemServerConn(t, func() imapserver.Session {
		return noACLSession{memServer.NewSession().(imapserver.SessionIMAP4rev2)}
	})
	t.Cleanup(func() { server.Close() })

	client := imapclient.New(conn, nil)
	t.Cleanup(func() { client.Close() })

	if err := client.Login(testUsername, testPassword).Wait(); err != nil {
		t.Fatalf("Login().Wait() = %v", err)
	}

	if client.Caps().Has(imap.CapACL) {
		t.Errorf("ACL capability advertised by a session without SessionACL")
	}

	tests := []struct {
		name string
		run  func() error
	}{
		{"SETACL", func() error {
			return client.SetACL("INBOX", "bob", imap.RightModificationReplace, imap.RightSet("lr")).Wait()
		}},
		{"DELETEACL", func() error { return client.DeleteACL("INBOX", "bob").Wait() }},
		{"GETACL", func() error { _, err := client.GetACL("INBOX").Wait(); return err }},
		{"LISTRIGHTS", func() error { _, err := client.ListRights("INBOX", "bob").Wait(); return err }},
		{"MYRIGHTS", func() error { _, err := client.MyRights("INBOX").Wait(); return err }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantIMAPError(t, tc.run(), imap.StatusResponseTypeBad, imap.ResponseCodeClientBug)
		})
	}

	// BAD does not close the connection
	if err := client.Noop().Wait(); err != nil {
		t.Fatalf("Noop().Wait() = %v", err)
	}
}
