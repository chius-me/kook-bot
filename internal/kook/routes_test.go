package kook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVoiceListUsesItemsOnly(t *testing.T) {
	cases := []struct {
		name string
		data string
		want int
	}{
		{name: "items", data: `{"items":[{"id":"20"},{"id":"23"},{"id":""}]}`, want: 2},
		{name: "missing", data: `{}`, want: 0},
		{name: "object", data: `{"items":{"id":"20"}}`, want: 0},
		{name: "other field", data: `{"sessions":[{"id":"9"}]}`, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v3/voice/list" {
					t.Errorf("path %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"code":0,"message":"操作成功","data":` + tc.data + `}`))
			}))
			defer srv.Close()
			ids, err := newTestClient(t, srv, nil).VoiceList(context.Background(), "tok")
			if err != nil {
				t.Fatal(err)
			}
			if len(ids) != tc.want {
				t.Fatalf("ids %#v", ids)
			}
			if tc.want == 2 && (ids[0] != "20" || ids[1] != "23") {
				t.Fatalf("ids %#v", ids)
			}
		})
	}
}
