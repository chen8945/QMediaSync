package requests

import "testing"

func TestStrmResultsPagination(t *testing.T) {
	for _, tt := range []struct {
		page, size int
		valid      bool
	}{{0, 0, true}, {1, 100, true}, {-1, 20, false}, {1, -1, false}, {1, 101, false}, {1000001, 1, false}} {
		req := StrmResultsRequest{Page: tt.page, PageSize: tt.size}
		if err := req.Validate(); (err == nil) != tt.valid {
			t.Fatalf("%+v: %v", tt, err)
		}
		if tt.page == 0 && (req.Page != 1 || req.PageSize != 20) {
			t.Fatalf("defaults: %+v", req)
		}
	}
}
