package v1

import (
	"encoding/json"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	req := Request{Version: Version, RequestID: "r1", Operation: OpPortsList}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != req.Version || got.RequestID != req.RequestID || got.Operation != req.Operation {
		t.Fatalf("got %+v, want %+v", got, req)
	}
}

func TestResponseWithErrorRoundTrip(t *testing.T) {
	resp := Response{Version: Version, RequestID: "r1", Error: &ProtocolError{Code: ErrCodePermissionDenied, Message: "no"}}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var got Response
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Error == nil || got.Error.Code != ErrCodePermissionDenied {
		t.Fatalf("%+v", got)
	}
}
