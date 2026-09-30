package helpers

import (
	"testing"

	"go.yaml.in/yaml/v3"
)

func BenchmarkConfigYAML(b *testing.B) {
	cfg := MakeDefaultConfig()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("decode", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var out Config
			if err := yaml.Unmarshal(data, &out); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("encode", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := yaml.Marshal(cfg); err != nil {
				b.Fatal(err)
			}
		}
	})
}
