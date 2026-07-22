package emby

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"qmediasync/emby302/util/jsons"
)

func TestDetectSubtitleStreamsDeliveryUrlBuildsExternalSubtitleURLs(t *testing.T) {
	source := mustSubtitleMediaSource(t, `{
		"Id":"media-source",
		"ItemId":"item-1",
		"MediaStreams":[
			{"Type":"Subtitle","IsExternal":true,"DeliveryMethod":"Encode","Codec":"ASS","Index":1,"IsTextSubtitleStream":true},
			{"Type":"Subtitle","IsExternal":true,"DeliveryMethod":"Encode","Codec":"PGSSUB","Index":2,"IsTextSubtitleStream":false},
			{"Type":"Subtitle","IsExternal":true,"DeliveryMethod":"Encode","Codec":"SUP","Index":3,"IsTextSubtitleStream":false},
			{"Type":"Subtitle","IsExternal":true,"DeliveryMethod":"External","DeliveryUrl":"/already-external","Codec":"srt","Index":4,"IsTextSubtitleStream":true},
			{"Type":"Subtitle","IsExternal":false,"DeliveryMethod":"Encode","Codec":"PGS","Index":5,"IsTextSubtitleStream":false},
			{"Type":"Subtitle","IsExternal":true,"DeliveryMethod":"Encode","Codec":"srt","Index":1.5,"IsTextSubtitleStream":true}
		]
	}`)
	const apiKey = "key with+symbols/="

	detectSubtitleStreamsDeliveryUrl(source, apiKey)

	assertSubtitleDeliveryURL(t, source, 0, "ass", 1, apiKey)
	assertSubtitleDeliveryURL(t, source, 1, "sup", 2, apiKey)
	assertSubtitleDeliveryURL(t, source, 2, "sup", 3, apiKey)

	mediaStreams, ok := source.Attr("MediaStreams").Done()
	if !ok {
		t.Fatal("missing MediaStreams")
	}

	pgsText, ok := mediaStreams.Idx(1).Attr("IsTextSubtitleStream").Bool()
	if !ok || pgsText {
		t.Fatalf("PGS/SUP must remain a graphic subtitle, got IsTextSubtitleStream=%v", pgsText)
	}

	if got, _ := mediaStreams.Idx(3).Attr("DeliveryMethod").String(); got != "External" {
		t.Fatalf("existing external subtitle changed DeliveryMethod: %q", got)
	}
	if got, _ := mediaStreams.Idx(3).Attr("DeliveryUrl").String(); got != "/already-external" {
		t.Fatalf("existing external subtitle changed DeliveryUrl: %q", got)
	}
	if got, _ := mediaStreams.Idx(4).Attr("DeliveryMethod").String(); got != "Encode" {
		t.Fatalf("embedded subtitle should stay unchanged: %q", got)
	}
	if got, ok := mediaStreams.Idx(5).Attr("DeliveryUrl").String(); ok || got != "" {
		t.Fatalf("subtitle with a fractional index should not get DeliveryUrl: %q", got)
	}
}

func assertSubtitleDeliveryURL(t *testing.T, source *jsons.Item, streamIndex int, format string, subtitleIndex int, apiKey string) {
	t.Helper()

	mediaStreams, ok := source.Attr("MediaStreams").Done()
	if !ok {
		t.Fatal("missing MediaStreams")
	}
	stream, ok := mediaStreams.Idx(streamIndex).Done()
	if !ok {
		t.Fatalf("missing MediaStream at index %d", streamIndex)
	}
	if got, _ := stream.Attr("DeliveryMethod").String(); got != "External" {
		t.Fatalf("DeliveryMethod = %q, want External", got)
	}

	deliveryURL, ok := stream.Attr("DeliveryUrl").String()
	if !ok {
		t.Fatal("missing DeliveryUrl")
	}
	if strings.Contains(deliveryURL, apiKey) {
		t.Fatalf("DeliveryUrl should URL-encode the API key: %s", deliveryURL)
	}

	u, err := url.Parse(deliveryURL)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := "/Videos/item-1/media-source/Subtitles/" + strconv.Itoa(subtitleIndex) + "/0/Stream." + format
	if u.Path != wantPath {
		t.Fatalf("DeliveryUrl path = %q, want %q", u.Path, wantPath)
	}
	if got := u.Query().Get(QueryApiKeyName); got != apiKey {
		t.Fatalf("DeliveryUrl API key = %q, want %q", got, apiKey)
	}
}

func mustSubtitleMediaSource(t *testing.T, raw string) *jsons.Item {
	t.Helper()

	source, err := jsons.New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return source
}
