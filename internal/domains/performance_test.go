package domains

import (
	"reflect"
	"testing"
)

func TestNormalizeEntriesKeepsIPURLsSeparateFromDomainCoverage(t *testing.T) {
	for _, tt := range []struct{ input, want []string }{
		{[]string{"https://192.168.1.1/path", "1.1"}, []string{"192.168.1.1", "1.1"}},
		{[]string{"https://192.168.1.1/path", "host.192.168.1.1"}, []string{"192.168.1.1", "host.192.168.1.1"}},
	} {
		if got := NormalizeEntries(tt.input); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("NormalizeEntries(%q) = %q, want %q", tt.input, got, tt.want)
		}
		got, err := normalizeDomainList(tt.input)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("normalizeDomainList(%q) = %q, %v; want %q", tt.input, got, err, tt.want)
		}
	}
}

func BenchmarkNormalizeRoutingEntry(b *testing.B) {
	for _, entry := range []string{"api.example.com", "192.168.1.1", "192.168.1.0/24", "https://example.com/path"} {
		b.Run(entry, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, _, err := NormalizeEntry(entry); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
