package dbbackup

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type memoryWebDAV struct {
	mu           sync.Mutex
	objects      map[string][]byte
	methods      []string
	authOK       bool
	tamperReads  bool
	tamperFinal  bool
	moveStatus   int
	publishMove  bool
	ignoreDelete bool
}

func (m *memoryWebDAV) RoundTrip(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.methods = append(m.methods, req.Method)
	user, pass, ok := req.BasicAuth()
	m.authOK = m.authOK || (ok && user == "backup" && pass == "secret")
	status := http.StatusNoContent
	var body []byte
	switch req.Method {
	case http.MethodPut:
		body, _ = io.ReadAll(req.Body)
		m.objects[req.URL.Path] = append([]byte(nil), body...)
		status = http.StatusCreated
	case http.MethodGet:
		var exists bool
		body, exists = m.objects[req.URL.Path]
		if !exists {
			status = http.StatusNotFound
		} else {
			body = append([]byte(nil), body...)
			if m.tamperReads && strings.Contains(req.URL.Path, ".partial.") && len(body) > 0 {
				body[0] ^= 0xff
			}
			if m.tamperFinal && !strings.Contains(req.URL.Path, ".partial.") && len(body) > 0 {
				body[0] ^= 0xff
			}
			status = http.StatusOK
		}
	case "MOVE":
		destination, _ := url.Parse(req.Header.Get("Destination"))
		body = m.objects[req.URL.Path]
		if m.moveStatus != 0 {
			status = m.moveStatus
			if m.publishMove {
				m.objects[destination.Path] = append([]byte(nil), body...)
			}
		} else {
			delete(m.objects, req.URL.Path)
			m.objects[destination.Path] = body
			status = http.StatusCreated
		}
	case http.MethodDelete:
		if !m.ignoreDelete {
			delete(m.objects, req.URL.Path)
		}
		status = http.StatusNoContent
	case "PROPFIND":
		status = http.StatusMultiStatus
	default:
		status = http.StatusMethodNotAllowed
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}, nil
}

func testWebDAVClient(server *memoryWebDAV) *WebDAVClient {
	origin, _ := url.Parse("https://dav.example")
	return &WebDAVClient{
		target: Target{Origin: origin, BasePath: "/pandora", username: "backup", password: "secret"},
		http:   &http.Client{Transport: server},
	}
}

func TestUploadVerifiedPublishesAfterRemoteReadback(t *testing.T) {
	server := &memoryWebDAV{objects: map[string][]byte{}}
	client := testWebDAVClient(server)
	if err := client.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	name := "aegis-postgres-20260801T031700Z.dump.age"
	path := filepath.Join(secureTempDir(t), name)
	payload := []byte("age-encrypted-backup")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := client.UploadVerified(context.Background(), path, name)
	if err != nil {
		t.Fatal(err)
	}
	if result.ObjectName != name || result.Bytes != int64(len(payload)) || len(result.SHA256) != 64 {
		t.Fatalf("result=%+v", result)
	}
	if !server.authOK || string(server.objects["/pandora/"+name]) != string(payload) {
		t.Fatalf("auth=%t objects=%v", server.authOK, server.objects)
	}
	for key := range server.objects {
		if strings.Contains(key, ".partial.") {
			t.Fatalf("partial object remained: %s", key)
		}
	}
	wantMethods := "PROPFIND,GET,PUT,GET,MOVE,GET,DELETE"
	if got := strings.Join(server.methods, ","); got != wantMethods {
		t.Fatalf("methods=%s want=%s", got, wantMethods)
	}
}

func TestUploadVerifiedRejectsTamperAndCleansPartial(t *testing.T) {
	server := &memoryWebDAV{objects: map[string][]byte{}, tamperReads: true}
	client := testWebDAVClient(server)
	name := "aegis-postgres-20260801T031701Z.dump.age"
	path := filepath.Join(secureTempDir(t), name)
	if err := os.WriteFile(path, []byte("age-encrypted-backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UploadVerified(context.Background(), path, name); err == nil {
		t.Fatal("tampered readback succeeded")
	}
	for key := range server.objects {
		if strings.Contains(key, ".partial.") {
			t.Fatalf("partial object remained: %s", key)
		}
	}
	if got := strings.Join(server.methods, ","); got != "GET,PUT,GET,DELETE" {
		t.Fatalf("methods=%s", got)
	}
}

func TestUploadVerifiedTreatsExactExistingFinalAsIdempotentSuccess(t *testing.T) {
	name := "aegis-postgres-20260801T031704Z.dump.age"
	payload := []byte("age-encrypted-backup")
	server := &memoryWebDAV{objects: map[string][]byte{"/pandora/" + name: payload}}
	client := testWebDAVClient(server)
	path := filepath.Join(secureTempDir(t), name)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UploadVerified(context.Background(), path, name); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(server.methods, ","); got != "GET" {
		t.Fatalf("methods=%s", got)
	}
}

func TestUploadVerifiedRejectsConflictingExistingFinal(t *testing.T) {
	name := "aegis-postgres-20260801T031705Z.dump.age"
	server := &memoryWebDAV{objects: map[string][]byte{"/pandora/" + name: []byte("other")}}
	client := testWebDAVClient(server)
	path := filepath.Join(secureTempDir(t), name)
	if err := os.WriteFile(path, []byte("age-encrypted-backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UploadVerified(context.Background(), path, name); err == nil {
		t.Fatal("conflicting final succeeded")
	}
	if got := strings.Join(server.methods, ","); got != "GET" {
		t.Fatalf("methods=%s", got)
	}
}

func TestUploadVerifiedRechecksMoveConflictAndAcceptsExactFinal(t *testing.T) {
	name := "aegis-postgres-20260801T031706Z.dump.age"
	payload := []byte("age-encrypted-backup")
	server := &memoryWebDAV{objects: map[string][]byte{}, moveStatus: http.StatusPreconditionFailed, publishMove: true}
	client := testWebDAVClient(server)
	path := filepath.Join(secureTempDir(t), name)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UploadVerified(context.Background(), path, name); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(server.methods, ","); got != "GET,PUT,GET,MOVE,GET,DELETE" {
		t.Fatalf("methods=%s", got)
	}
}

func TestUploadVerifiedDeletesFinalCreatedByThisRunWhenReadbackFails(t *testing.T) {
	name := "aegis-postgres-20260801T031707Z.dump.age"
	server := &memoryWebDAV{objects: map[string][]byte{}, tamperFinal: true}
	client := testWebDAVClient(server)
	path := filepath.Join(secureTempDir(t), name)
	if err := os.WriteFile(path, []byte("age-encrypted-backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UploadVerified(context.Background(), path, name); err == nil {
		t.Fatal("tampered final succeeded")
	}
	if _, exists := server.objects["/pandora/"+name]; exists {
		t.Fatal("invalid final remained")
	}
	for key := range server.objects {
		if strings.Contains(key, ".partial.") {
			t.Fatalf("partial object remained: %s", key)
		}
	}
	if got := strings.Join(server.methods, ","); got != "GET,PUT,GET,MOVE,GET,DELETE,DELETE" {
		t.Fatalf("methods=%s", got)
	}
}

func TestUploadVerifiedBytesPublishesManifestLastObjectType(t *testing.T) {
	server := &memoryWebDAV{objects: map[string][]byte{}}
	client := testWebDAVClient(server)
	payload := []byte("{\"signed\":true}\n")
	name := "aegis-postgres-20260801T031708Z.manifest.json"
	result, err := client.UploadVerifiedBytes(context.Background(), name, payload)
	if err != nil {
		t.Fatal(err)
	}
	if result.ObjectName != name || result.Bytes != int64(len(payload)) {
		t.Fatalf("result=%+v", result)
	}
	if got := strings.Join(server.methods, ","); got != "GET,PUT,GET,MOVE,GET,DELETE" {
		t.Fatalf("methods=%s", got)
	}
}

func TestUploadVerifiedDigestRefusesChangedLocalArtifactBeforeNetwork(t *testing.T) {
	server := &memoryWebDAV{objects: map[string][]byte{}}
	client := testWebDAVClient(server)
	name := "aegis-postgres-20260801T031703Z.dump.age"
	path := filepath.Join(secureTempDir(t), name)
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UploadVerifiedDigest(context.Background(), path, name,
		"0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("changed local artifact succeeded")
	}
	if len(server.methods) != 0 {
		t.Fatalf("network methods=%v", server.methods)
	}
}

func TestUploadVerifiedRejectsUntrustedNamesAndSymlinks(t *testing.T) {
	client := testWebDAVClient(&memoryWebDAV{objects: map[string][]byte{}})
	dir := secureTempDir(t)
	regular := filepath.Join(dir, "backup.age")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UploadVerified(context.Background(), regular, "../../backup.age"); err == nil {
		t.Fatal("unsafe object name succeeded")
	}
	link := filepath.Join(dir, "aegis-postgres-20260801T031702Z.dump.age")
	if err := os.Symlink(regular, link); err == nil {
		if _, err := client.UploadVerified(context.Background(), link, filepath.Base(link)); err == nil {
			t.Fatal("symlink upload succeeded")
		}
	}
}
