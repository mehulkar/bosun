package search

import (
	"testing"
	"time"

	"bosun.org/opentsdb"
)

var testSearch = NewSearch()

func i(t *testing.T, metric, tags string) {
	ts, err := opentsdb.ParseTags(tags)
	if err != nil {
		t.Fatal(err)
	}
	dp := &opentsdb.DataPoint{Metric: metric, Tags: ts}
	testSearch.Index(opentsdb.MultiDataPoint{dp})

}

func flush() {
	time.Sleep(5 * time.Millisecond)
	testSearch.Copy()
}

func TestUniqueMetrics(t *testing.T) {
	i(t, "os.cpu", "host=a")
	i(t, "os.cpu", "host=b")
	i(t, "os.mem", "host=a")
	flush()
	metrics := testSearch.UniqueMetrics()
	if len(metrics) != 2 {
		t.Fatalf("Expected 2 metrics but found %d", len(metrics))
	}
	if metrics[0] == metrics[1] {
		t.Fatal("Duplicate metrics found")
	}
}
