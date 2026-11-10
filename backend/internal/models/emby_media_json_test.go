package models

import (
	"encoding/json"
	"testing"
)

func TestEmbyMediaSyncFileLegacyIntegerJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"zero", `0`, "0"},
		{"ordinary_integer", `101`, "101"},
		{"above_float_precision", `9007199254740993`, "9007199254740993"},
		{"max_int64", `9223372036854775807`, "9223372036854775807"},
		{"max_uint64", `18446744073709551615`, "18446744073709551615"},
		{"numeric_string", `"00101"`, "00101"},
		{"guid_string", `"01234567-89AB-CDEF-0123-456789abcdef"`, "01234567-89AB-CDEF-0123-456789abcdef"},
		{"empty_string", `""`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got EmbyMediaSyncFile
			data := `{"id":7,"created_at":11,"updated_at":12,"sync_path_id":3,"emby_item_id":` + tc.raw + `,"sync_file_id":5,"pick_code":"pick","snapshot_id":9,"source_id":"source"}`
			if err := json.Unmarshal([]byte(data), &got); err != nil {
				t.Fatal(err)
			}
			want := EmbyMediaSyncFile{BaseModel: BaseModel{ID: 7, CreatedAt: 11, UpdatedAt: 12}, SyncPathId: 3, EmbyItemId: tc.want, SyncFileId: 5, PickCode: "pick", SnapshotID: 9, SourceID: "source"}
			if got != want {
				t.Fatalf("decoded link=%+v, want %+v", got, want)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			var id string
			if err := json.Unmarshal(fields["emby_item_id"], &id); err != nil || id != tc.want {
				t.Fatalf("new JSON must contain string ID %q: %s (err=%v)", tc.want, encoded, err)
			}
		})
	}
}

func TestEmbyMediaSyncFileRejectsInvalidIntegerJSON(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"negative", `-1`},
		{"negative_zero", `-0`},
		{"uint64_overflow", `18446744073709551616`},
		{"fraction", `1.5`},
		{"decimal", `1.0`},
		{"exponent", `1e2`},
		{"boolean", `true`},
		{"object", `{}`},
		{"array", `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var link EmbyMediaSyncFile
			if err := json.Unmarshal([]byte(`{"emby_item_id":`+tc.raw+`}`), &link); err == nil {
				t.Fatalf("accepted invalid integer identity: %s", tc.raw)
			}
		})
	}
}

func TestEmbyMediaSyncFileNullAndOmittedJSON(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"null", `{"emby_item_id":null}`},
		{"omitted", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, initial := range []string{"", "existing-id"} {
				link := EmbyMediaSyncFile{EmbyItemId: initial}
				if err := json.Unmarshal([]byte(tc.raw), &link); err != nil {
					t.Fatal(err)
				}
				if link.EmbyItemId != initial {
					t.Fatalf("ID=%q, want unchanged %q", link.EmbyItemId, initial)
				}
			}
		})
	}
}
