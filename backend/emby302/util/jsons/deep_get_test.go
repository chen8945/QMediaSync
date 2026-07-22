package jsons_test

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"

	"qmediasync/emby302/util/jsons"
)

const largeJSONInteger int64 = 9007199254740993

func TestTempItemIntAcceptsWholeNumbersWithinIntRange(t *testing.T) {
	tests := []struct {
		name string
		item *jsons.Item
		want int
	}{
		{
			name: "JSON integer",
			item: mustItemAt(t, `{"value":42}`, "value"),
			want: 42,
		},
		{
			name: "int64 value",
			item: jsons.FromValue(int64(99)),
			want: 99,
		},
		{
			name: "minimum int value",
			item: jsons.FromValue(float64(-math.Ldexp(1, strconv.IntSize-1))),
			want: -1 << (strconv.IntSize - 1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.item.Ti().Int()
			if !ok {
				t.Fatal("Int() 应接受范围内的整数")
			}
			if got != tt.want {
				t.Fatalf("Int() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTempItemIntRejectsInvalidNumbers(t *testing.T) {
	tests := []struct {
		name string
		item *jsons.Item
	}{
		{
			name: "fractional number",
			item: jsons.FromValue(1.5),
		},
		{
			name: "NaN",
			item: jsons.FromValue(math.NaN()),
		},
		{
			name: "positive infinity",
			item: jsons.FromValue(math.Inf(1)),
		},
		{
			name: "negative infinity",
			item: jsons.FromValue(math.Inf(-1)),
		},
		{
			name: "positive overflow",
			item: jsons.FromValue(math.Ldexp(1, strconv.IntSize-1)),
		},
		{
			name: "negative overflow",
			item: jsons.FromValue(-math.Ldexp(1, strconv.IntSize)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := tt.item.Ti().Int(); ok {
				t.Fatalf("Int() = %d, want conversion rejected", got)
			}
		})
	}
}

func TestTempItemIntPreservesJSONIntegerPrecision(t *testing.T) {
	item := mustItemAt(t, `{"value":9007199254740993}`, "value")

	got, ok := item.Ti().Int()
	if strconv.IntSize == 64 {
		if !ok {
			t.Fatal("Int() 应接受 int 范围内的 JSON 整数")
		}
		if int64(got) != largeJSONInteger {
			t.Fatalf("Int() = %d, want %d", got, largeJSONInteger)
		}
		return
	}

	if ok {
		t.Fatalf("Int() = %d, want conversion rejected on 32-bit platforms", got)
	}
}

func TestTempItemIntRejectsJSONIntegerOverflow(t *testing.T) {
	item := mustItemAt(t, `{"value":-9223372036854775809}`, "value")

	if got, ok := item.Ti().Int(); ok {
		t.Fatalf("Int() = %d, want conversion rejected", got)
	}
}

func TestTempItemInt64PreservesJSONIntegerPrecision(t *testing.T) {
	item := mustItemAt(t, `{"value":9007199254740993}`, "value")

	got, ok := item.Ti().Int64()
	if !ok {
		t.Fatal("Int64() 应接受 int64 范围内的 JSON 整数")
	}
	if got != largeJSONInteger {
		t.Fatalf("Int64() = %d, want %d", got, largeJSONInteger)
	}
}

func TestTempItemInt64RejectsJSONIntegerOverflow(t *testing.T) {
	item := mustItemAt(t, `{"value":-9223372036854775809}`, "value")

	if got, ok := item.Ti().Int64(); ok {
		t.Fatalf("Int64() = %d, want conversion rejected", got)
	}
}

func TestTempItemFloatReadsJSONNumber(t *testing.T) {
	item := mustItemAt(t, `{"value":1.5}`, "value")

	got, ok := item.Ti().Float()
	if !ok {
		t.Fatal("Float() 应接受 JSON 浮点数")
	}
	if got != 1.5 {
		t.Fatalf("Float() = %v, want 1.5", got)
	}
}

func TestNewPreservesJSONNumberWhenMarshaling(t *testing.T) {
	const raw = `{"value":9007199254740993}`

	item, err := jsons.New(raw)
	if err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); got != raw {
		t.Fatalf("MarshalJSON() = %s, want %s", got, raw)
	}
}

func TestNewRejectsTrailingJSONValue(t *testing.T) {
	if _, err := jsons.New(`1 2`); err == nil {
		t.Fatal("New() 应拒绝多个顶层 JSON 值")
	}
}

func mustItemAt(t *testing.T, raw, key string) *jsons.Item {
	t.Helper()

	item, err := jsons.New(raw)
	if err != nil {
		t.Fatal(err)
	}

	value, ok := item.Attr(key).Done()
	if !ok {
		t.Fatalf("未找到 JSON 字段 %q", key)
	}
	return value
}
