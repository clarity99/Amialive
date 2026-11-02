package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBulkGateSend(t *testing.T) {
	var got map[string]any
	status, reply := 200, `{"data":{"status":"accepted"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		w.Write([]byte(reply))
	}))
	defer srv.Close()
	g := &BulkGate{"451", "tok", "gText", "Amialive", srv.URL}

	if err := g.Send("+447700900000", "Rob ✅"); err != nil {
		t.Fatal(err)
	}
	if got["number"] != "447700900000" || got["unicode"] != true || got["sender_id_value"] != "Amialive" || got["application_id"] != "451" {
		t.Fatalf("bad request: %v", got)
	}

	status, reply = 401, `{"type":"unknown_identity","code":401,"error":"Unknown identity"}`
	if err := g.Send("+447700900000", "x"); err == nil {
		t.Fatal("want error on 401")
	}
	status, reply = 200, `{"data":{"status":"rejected"}}`
	if err := g.Send("+447700900000", "x"); err == nil {
		t.Fatal("want error on non-accepted status")
	}
}
