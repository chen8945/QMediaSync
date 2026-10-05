package requests

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// 以下为构造的边界输入，不冒充 Emby 真实捕获。
func TestParseEmbyWebhookRejectsAmbiguousInput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, body string }{
		{"invalid_json", `{"Event":`},
		{"multiple_objects", `{"Event":"system.test"}{}`},
		{"not_object", `[]`},
		{"null_object", `null`},
		{"missing_event", `{}`},
		{"duplicate_event", `{"Event":"library.new","Event":"library.deleted"}`},
		{"case_duplicate", `{"Event":"library.new","event":"library.deleted"}`},
		{"wrong_key_case", `{"event":"system.test"}`},
		{"null_event", `{"Event":null}`},
		{"nonstring_event", `{"Event":1}`},
		{"missing_item", `{"Event":"library.deleted"}`},
		{"null_item", `{"Event":"library.deleted","Item":null}`},
		{"nonobject_item", `{"Event":"library.deleted","Item":[]}`},
		{"duplicate_item_id", `{"Event":"library.deleted","Item":{"Id":"1","Id":"2","Type":"Movie"}}`},
		{"case_duplicate_item_path", `{"Event":"library.deleted","Item":{"Id":"1","Type":"Movie","Path":"/a","path":"/b"}}`},
		{"null_item_path", `{"Event":"library.deleted","Item":{"Id":"1","Type":"Movie","Path":null}}`},
		{"nonstring_item_id", `{"Event":"library.deleted","Item":{"Id":1,"Type":"Movie"}}`},
		{"empty_item_id", `{"Event":"library.deleted","Item":{"Id":"","Type":"Movie"}}`},
		{"overflow_item_id", `{"Event":"library.deleted","Item":{"Id":"9223372036854775808","Type":"Movie"}}`},
		{"noncanonical_item_id", `{"Event":"library.deleted","Item":{"Id":"01","Type":"Movie"}}`},
		{"invalid_parent_id", `{"Event":"library.deleted","Item":{"Id":"1","Type":"Movie","ParentId":"../../"}}`},
		{"fractional_index", `{"Event":"library.deleted","Item":{"Id":"1","Type":"Movie","IndexNumber":1.5}}`},
		{"null_folder", `{"Event":"library.deleted","Item":{"Id":"1","Type":"Movie","IsFolder":null}}`},
		{"nonobject_server", `{"Event":"system.test","Server":[]}`},
		{"duplicate_server_id", `{"Event":"system.test","Server":{"Id":"a","Id":"b"}}`},
		{"invalid_date", `{"Event":"library.deleted","Date":"not a date","Item":{"Id":"1","Type":"Movie"}}`},
		{"nul_path", `{"Event":"library.deleted","Item":{"Id":"1","Type":"Movie","Path":"/a\u0000b"}}`},
		{"isolated_high_surrogate", `{"Event":"library.deleted","Item":{"Id":"1","Type":"Movie","Path":"/a\ud800b"}}`},
		{"isolated_low_surrogate", `{"Event":"library.deleted","Item":{"Id":"1","Type":"Movie","Path":"/a\udc00b"}}`},
		{"too_deep", `{"Event":"system.test","unknown":` + strings.Repeat("[", 66) + `0` + strings.Repeat("]", 66) + `}`},
		{"too_large", `{"Event":"system.test","unknown":"` + strings.Repeat("a", EmbyWebhookMaxBytes) + `"}`},
		{"invalid_utf8", "{\"Event\":\"system.test\",\"unknown\":\"\xff\"}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseEmbyWebhook([]byte(test.body)); err == nil {
				t.Fatal("ambiguous input must be rejected")
			}
		})
	}
}

func TestEmbyWebhookPreservesValidUnicodeAndLegacyPlayback(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"Event":"playback.start","User":null,"Item":{"ImageTags":null,"Genres":null}}`,
		`{"Event":"library.modified","Item":{"Id":"1","Type":"Movie","Path":"/media/\ud83d\ude00.strm"}}`,
		`{"Event":"library.modified","Item":{"Id":"1","Type":"Movie","Path":"/media/\\ud800.strm"}}`,
	} {
		if _, err := ParseEmbyWebhook([]byte(body)); err != nil {
			t.Fatalf("valid Unicode or legacy nullable display fields rejected: %v", err)
		}
	}
}

func TestEmbyWebhookEnvelopeKeepsPhysicalPathAndTopology(t *testing.T) {
	t.Parallel()
	request, err := ParseEmbyWebhook([]byte(`{"Event":"library.modified","Date":"2026-10-04T15:31:39.3464217Z","Server":{"Id":"server"},"Item":{"Id":"1","ServerId":"server","Type":"Episode","ParentId":"2","SeriesId":"3","SeasonId":"2","IndexNumber":0,"Path":"/media/中文, [版本]\n第二行.strm"}}`))
	if err != nil {
		t.Fatal(err)
	}
	input := request.ToEnvelope()
	if !request.Managed() || input.Blocked || input.ItemPath != "/media/中文, [版本]\n第二行.strm" || input.SeriesID != "3" || input.SeasonID != "2" || input.ParentID != "2" {
		t.Fatalf("physical input was rewritten: %+v", input)
	}
	if input.IndexNumber == nil || *input.IndexNumber != 0 || input.ParentIndexNumber != nil {
		t.Fatal("Specials index zero and absent parent index must remain distinct")
	}
	for _, test := range []struct{ name, body, issue string }{
		{"missing_server", `{"Event":"library.new","Item":{"Id":"1","Type":"Movie"}}`, "missing_server_identity"},
		{"server_conflict", `{"Event":"library.deleted","Server":{"Id":"a"},"Item":{"Id":"1","ServerId":"b","Type":"Movie","Path":"/a"}}`, "conflicting_server_identity"},
		{"missing_original_path", `{"Event":"library.deleted","Server":{"Id":"a"},"Item":{"Id":"1","Type":"Movie"}}`, "missing_original_item_path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := ParseEmbyWebhook([]byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			input := request.ToEnvelope()
			if !input.Blocked || !slices.Contains(input.Issues, test.issue) {
				t.Fatalf("identity issue must persist without execution: %+v", input)
			}
		})
	}
}

func TestEmbyDeepDescriptionConservativeCandidates(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, description, source, issue, candidate, pickcode string
	}{
		{"local_with_spaces_comma", "Item Name:\nname\n\nItem Path:\n/media/movie\n\nMount Paths:\n/drive/ name, [version].mp4 ", "deep-description-v1", "", "/drive/ name, [version].mp4 ", ""},
		{"literal_backslash_n", `Item Name:` + "\nname\n\nItem Path:\n/media/movie\n\nMount Paths:\n" + `/drive/name\npart.mp4`, "deep-description-v1", "", `/drive/name\npart.mp4`, ""},
		{"credential_url", "Item Name:\nname\n\nItem Path:\n/media/movie\n\nMount Paths:\nhttps://user:password@media.invalid/source.mp4?api_key=secret&token=token-secret&pickcode=fixture-code#credential", "deep-description-v1", "", "https://media.invalid/source.mp4", "fixture-code"},
		{"local_multiple_or_newline", "Item Name:\nname\n\nItem Path:\n/media/movie\n\nMount Paths:\n/drive/one\n/drive/two", "deep-description-v1", "ambiguous_multiline_mount_paths", "", ""},
		{"multiple_http", "Item Name:\nname\n\nItem Path:\n/media/movie\n\nMount Paths:\nhttps://one.invalid/a?token=secret\nhttps://two.invalid/b", "deep-description-v1", "ambiguous_multiline_mount_paths", "", ""},
		{"relative", "Item Name:\nname\n\nItem Path:\n/media/movie\n\nMount Paths:\nrelative.mp4", "deep-description-v1", "unsupported_mount_path", "", ""},
		{"unsupported_scheme", "Item Name:\nname\n\nItem Path:\n/media/movie\n\nMount Paths:\nftp://host/a", "deep-description-v1", "unsupported_mount_path", "", ""},
		{"missing_header", "Mount Paths:\n/drive/a", "deep-unsupported", "unsupported_deep_description", "", ""},
		{"duplicate_header", "Item Name:\nname\n\nItem Path:\n/media/movie\n\nMount Paths:\n/drive/a\n\nMount Paths:\n/drive/b", "deep-unsupported", "unsupported_deep_description", "", ""},
		{"crlf_constructed", "Item Name:\r\nname\r\n\r\nItem Path:\r\n/media/movie\r\n\r\nMount Paths:\r\n/drive/a", "deep-unsupported", "unsupported_deep_description", "", ""},
		{"conflicting_pickcode", "Item Name:\nname\n\nItem Path:\n/media/movie\n\nMount Paths:\nhttps://media.invalid/a?pickcode=one&pick_code=two", "deep-description-v1", "conflicting_pickcode_candidates", "", ""},
		{"duplicate_pickcode", "Item Name:\nname\n\nItem Path:\n/media/movie\n\nMount Paths:\nhttps://media.invalid/a?pickcode=one&pickcode=two", "deep-description-v1", "conflicting_pickcode_candidates", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"Event": "deep.delete", "Description": test.description, "Server": map[string]string{"Id": "server"}, "Item": map[string]string{"Id": "1", "Type": "Movie", "Path": "/media/movie/file.strm"}})
			if err != nil {
				t.Fatal(err)
			}
			request, err := ParseEmbyWebhook(body)
			if err != nil {
				t.Fatal(err)
			}
			input := request.ToEnvelope()
			if input.Source != test.source || input.Blocked || input.ItemPath != "/media/movie/file.strm" {
				t.Fatalf("unexpected input: %+v", input)
			}
			if test.issue != "" && !slices.Contains(input.Issues, test.issue) {
				t.Fatalf("missing issue %s: %+v", test.issue, input)
			}
			if test.candidate == "" {
				if len(input.Candidates) != 0 {
					t.Fatalf("ambiguous source became candidates: %+v", input.Candidates)
				}
			} else if len(input.Candidates) != 1 || input.Candidates[0].Path != test.candidate || input.Candidates[0].PickCode != test.pickcode {
				t.Fatalf("source candidate was split or lost identity: %+v", input.Candidates)
			}
			stored, _ := json.Marshal(input)
			for _, secret := range []string{"password", "api_key", "token-secret", "#credential"} {
				if strings.Contains(string(stored), secret) {
					t.Fatalf("stored input contains credential: %s", secret)
				}
			}
		})
	}
}
