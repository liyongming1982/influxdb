package tsi1_test

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	"github.com/influxdata/influxdb/models"
	"github.com/influxdata/influxdb/tsdb"
	"github.com/influxdata/influxdb/tsdb/index/tsi1"
)

// Bloom filter settings used in tests.
const M, K = 4096, 6

// Ensure index can iterate over all measurement names.
func TestIndex_ForEachMeasurementName(t *testing.T) {
	sfile := MustOpenSeriesFile()
	defer sfile.Close()

	idx := MustOpenIndex(sfile.SeriesFile, 1)
	defer idx.Close()

	// Add series to index.
	if err := idx.CreateSeriesSliceIfNotExists([]Series{
		{Name: []byte("cpu"), Tags: models.NewTags(map[string]string{"region": "east"})},
		{Name: []byte("cpu"), Tags: models.NewTags(map[string]string{"region": "west"})},
		{Name: []byte("mem"), Tags: models.NewTags(map[string]string{"region": "east"})},
	}); err != nil {
		t.Fatal(err)
	}

	// Verify measurements are returned.
	idx.Run(t, func(t *testing.T) {
		var names []string
		if err := idx.ForEachMeasurementName(func(name []byte) error {
			names = append(names, string(name))
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(names, []string{"cpu", "mem"}) {
			t.Fatalf("unexpected names: %#v", names)
		}
	})

	// Add more series.
	if err := idx.CreateSeriesSliceIfNotExists([]Series{
		{Name: []byte("disk")},
		{Name: []byte("mem")},
	}); err != nil {
		t.Fatal(err)
	}

	// Verify new measurements.
	idx.Run(t, func(t *testing.T) {
		var names []string
		if err := idx.ForEachMeasurementName(func(name []byte) error {
			names = append(names, string(name))
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(names, []string{"cpu", "disk", "mem"}) {
			t.Fatalf("unexpected names: %#v", names)
		}
	})
}

// Ensure index can return whether a measurement exists.
func TestIndex_MeasurementExists(t *testing.T) {
	sfile := MustOpenSeriesFile()
	defer sfile.Close()

	idx := MustOpenIndex(sfile.SeriesFile, 1)
	defer idx.Close()

	// Add series to index.
	if err := idx.CreateSeriesSliceIfNotExists([]Series{
		{Name: []byte("cpu"), Tags: models.NewTags(map[string]string{"region": "east"})},
		{Name: []byte("cpu"), Tags: models.NewTags(map[string]string{"region": "west"})},
	}); err != nil {
		t.Fatal(err)
	}

	// Verify measurement exists.
	idx.Run(t, func(t *testing.T) {
		if v, err := idx.MeasurementExists([]byte("cpu")); err != nil {
			t.Fatal(err)
		} else if !v {
			t.Fatal("expected measurement to exist")
		}
	})

	// Delete one series.
	if err := idx.DropSeries(models.MakeKey([]byte("cpu"), models.NewTags(map[string]string{"region": "east"})), 0); err != nil {
		t.Fatal(err)
	}

	// Verify measurement still exists.
	idx.Run(t, func(t *testing.T) {
		if v, err := idx.MeasurementExists([]byte("cpu")); err != nil {
			t.Fatal(err)
		} else if !v {
			t.Fatal("expected measurement to still exist")
		}
	})

	// Delete second series.
	if err := idx.DropSeries(models.MakeKey([]byte("cpu"), models.NewTags(map[string]string{"region": "west"})), 0); err != nil {
		t.Fatal(err)
	}

	// Verify measurement is now deleted.
	idx.Run(t, func(t *testing.T) {
		if v, err := idx.MeasurementExists([]byte("cpu")); err != nil {
			t.Fatal(err)
		} else if v {
			t.Fatal("expected measurement to be deleted")
		}
	})
}

// Ensure index can return a list of matching measurements.
func TestIndex_MeasurementNamesByRegex(t *testing.T) {
	sfile := MustOpenSeriesFile()
	defer sfile.Close()

	idx := MustOpenIndex(sfile.SeriesFile, 1)
	defer idx.Close()

	// Add series to index.
	if err := idx.CreateSeriesSliceIfNotExists([]Series{
		{Name: []byte("cpu")},
		{Name: []byte("disk")},
		{Name: []byte("mem")},
	}); err != nil {
		t.Fatal(err)
	}

	// Retrieve measurements by regex.
	idx.Run(t, func(t *testing.T) {
		names, err := idx.MeasurementNamesByRegex(regexp.MustCompile(`cpu|mem`))
		if err != nil {
			t.Fatal(err)
		} else if !reflect.DeepEqual(names, [][]byte{[]byte("cpu"), []byte("mem")}) {
			t.Fatalf("unexpected names: %v", names)
		}
	})
}

// Ensure index can delete a measurement and all related keys, values, & series.
func TestIndex_DropMeasurement(t *testing.T) {
	sfile := MustOpenSeriesFile()
	defer sfile.Close()

	idx := MustOpenIndex(sfile.SeriesFile, 1)
	defer idx.Close()

	// Add series to index.
	if err := idx.CreateSeriesSliceIfNotExists([]Series{
		{Name: []byte("cpu"), Tags: models.NewTags(map[string]string{"region": "east"})},
		{Name: []byte("cpu"), Tags: models.NewTags(map[string]string{"region": "west"})},
		{Name: []byte("disk"), Tags: models.NewTags(map[string]string{"region": "north"})},
		{Name: []byte("mem"), Tags: models.NewTags(map[string]string{"region": "west", "country": "us"})},
	}); err != nil {
		t.Fatal(err)
	}

	// Drop measurement.
	if err := idx.DropMeasurement([]byte("cpu")); err != nil {
		t.Fatal(err)
	}

	// Verify data is gone in each stage.
	idx.Run(t, func(t *testing.T) {
		// Verify measurement is gone.
		if v, err := idx.MeasurementExists([]byte("cpu")); err != nil {
			t.Fatal(err)
		} else if v {
			t.Fatal("expected no measurement")
		}

		// Obtain file set to perform lower level checks.
		fs := idx.PartitionAt(0).RetainFileSet()
		defer fs.Release()

		// Verify tags & values are gone.
		if e := fs.TagKeyIterator([]byte("cpu")).Next(); e != nil && !e.Deleted() {
			t.Fatal("expected deleted tag key")
		}
		if itr := fs.TagValueIterator([]byte("cpu"), []byte("region")); itr != nil {
			t.Fatal("expected nil tag value iterator")
		}

	})
}

func TestIndex_Open(t *testing.T) {
	sfile := MustOpenSeriesFile()
	defer sfile.Close()

	// Opening a fresh index should set the MANIFEST version to current version.
	idx := NewIndex(sfile.SeriesFile)
	t.Run("open new index", func(t *testing.T) {
		if err := idx.Open(); err != nil {
			t.Fatal(err)
		}

		// Check version set appropriately.
		if got, exp := idx.PartitionAt(0).Manifest().Version, 1; got != exp {
			t.Fatalf("got index version %d, expected %d", got, exp)
		}
	})

	// Reopening an open index should return an error.
	t.Run("reopen open index", func(t *testing.T) {
		err := idx.Open()
		if err == nil {
			idx.Close()
			t.Fatal("didn't get an error on reopen, but expected one")
		}
		idx.Close()
	})

	// Opening an incompatible index should return an error.
	incompatibleVersions := []int{-1, 0, 2}
	for _, v := range incompatibleVersions {
		t.Run(fmt.Sprintf("incompatible index version: %d", v), func(t *testing.T) {
			idx = NewIndex(sfile.SeriesFile)

			// Manually create a MANIFEST file for an incompatible index version.
			mpath := filepath.Join(idx.Path(), "0", tsi1.ManifestFileName)
			m := tsi1.NewManifest(mpath)
			m.Levels = nil
			m.Version = v // Set example MANIFEST version.
			if err := os.MkdirAll(filepath.Dir(mpath), 0777); err != nil {
				t.Fatal(err)
			} else if err := m.Write(); err != nil {
				t.Fatal(err)
			}

			// Log the MANIFEST file.
			data, err := ioutil.ReadFile(mpath)
			if err != nil {
				panic(err)
			}
			t.Logf("Incompatible MANIFEST: %s", data)

			// Opening this index should return an error because the MANIFEST has an
			// incompatible version.
			err = idx.Open()
			if err != tsi1.ErrIncompatibleVersion {
				idx.Close()
				t.Fatalf("got error %v, expected %v", err, tsi1.ErrIncompatibleVersion)
			}
		})
	}
}

func TestIndex_Manifest(t *testing.T) {
	t.Run("current MANIFEST", func(t *testing.T) {
		sfile := MustOpenSeriesFile()
		defer sfile.Close()

		idx := MustOpenIndex(sfile.SeriesFile, 1)
		if got, exp := idx.PartitionAt(0).Manifest().Version, tsi1.Version; got != exp {
			t.Fatalf("got MANIFEST version %d, expected %d", got, exp)
		}
	})
}

func TestIndex_DiskSizeBytes(t *testing.T) {
	sfile := MustOpenSeriesFile()
	defer sfile.Close()

	idx := MustOpenIndex(sfile.SeriesFile, 1)
	defer idx.Close()

	// Add series to index.
	if err := idx.CreateSeriesSliceIfNotExists([]Series{
		{Name: []byte("cpu"), Tags: models.NewTags(map[string]string{"region": "east"})},
		{Name: []byte("cpu"), Tags: models.NewTags(map[string]string{"region": "west"})},
		{Name: []byte("disk"), Tags: models.NewTags(map[string]string{"region": "north"})},
		{Name: []byte("mem"), Tags: models.NewTags(map[string]string{"region": "west", "country": "us"})},
	}); err != nil {
		t.Fatal(err)
	}

	// Verify on disk size is the same in each stage.
	expSize := int64(520) // 419 bytes for MANIFEST and 101 bytes for index file
	idx.Run(t, func(t *testing.T) {
		if got, exp := idx.DiskSizeBytes(), expSize; got != exp {
			t.Fatalf("got %d bytes, expected %d", got, exp)
		}
	})
}

// Index is a test wrapper for tsi1.Index.
type Index struct {
	*tsi1.Index
}

// NewIndex returns a new instance of Index at a temporary path.
func NewIndex(sfile *tsdb.SeriesFile) *Index {
	return &Index{Index: tsi1.NewIndex(sfile, tsi1.WithPath(MustTempDir()))}
}

// MustOpenIndex returns a new, open index. Panic on error.
func MustOpenIndex(sfile *tsdb.SeriesFile, partitionN uint64) *Index {
	idx := NewIndex(sfile)
	idx.PartitionN = partitionN
	if err := idx.Open(); err != nil {
		panic(err)
	}
	return idx
}

// Close closes and removes the index directory.
func (idx *Index) Close() error {
	defer os.RemoveAll(idx.Path())
	return idx.Index.Close()
}

// Reopen closes and opens the index.
func (idx *Index) Reopen() error {
	if err := idx.Index.Close(); err != nil {
		return err
	}

	sfile := idx.SeriesFile()
	path, partitionN := idx.Path(), idx.PartitionN

	idx.Index = tsi1.NewIndex(sfile, tsi1.WithPath(path))
	idx.PartitionN = partitionN
	if err := idx.Open(); err != nil {
		return err
	}
	return nil
}

// Run executes a subtest for each of several different states:
//
// - Immediately
// - After reopen
// - After compaction
// - After reopen again
//
// The index should always respond in the same fashion regardless of
// how data is stored. This helper allows the index to be easily tested
// in all major states.
func (idx *Index) Run(t *testing.T, fn func(t *testing.T)) {
	// Invoke immediately.
	t.Run("state=initial", fn)

	// Reopen and invoke again.
	if err := idx.Reopen(); err != nil {
		t.Fatalf("reopen error: %s", err)
	}
	t.Run("state=reopen", fn)

	// TODO: Request a compaction.
	// if err := idx.Compact(); err != nil {
	// 	t.Fatalf("compact error: %s", err)
	// }
	// t.Run("state=post-compaction", fn)

	// Reopen and invoke again.
	if err := idx.Reopen(); err != nil {
		t.Fatalf("post-compaction reopen error: %s", err)
	}
	t.Run("state=post-compaction-reopen", fn)
}

// CreateSeriesSliceIfNotExists creates multiple series at a time.
func (idx *Index) CreateSeriesSliceIfNotExists(a []Series) error {
	for i, s := range a {
		if err := idx.CreateSeriesListIfNotExists(nil, [][]byte{s.Name}, []models.Tags{s.Tags}); err != nil {
			return fmt.Errorf("i=%d, name=%s, tags=%v, err=%s", i, s.Name, s.Tags, err)
		}
	}
	return nil
}

func BytesToStrings(a [][]byte) []string {
	s := make([]string, 0, len(a))
	for _, v := range a {
		s = append(s, string(v))
	}
	return s
}
