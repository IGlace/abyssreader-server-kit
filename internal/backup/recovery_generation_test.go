// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIncompleteHasSeparateRetentionScheduleAndPreservationExport(t *testing.T) {
	d, db, name := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var good []string
	for i := 0; i < 3; i++ {
		dir, err := Create(ctx, d, db, 3, now.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		good = append(good, dir)
	}
	// Add an unavailable reference while leaving the healthy page for preservation.
	absent := fmt.Sprintf("%x.png", sha256.Sum256([]byte("unavailable")))
	if _, err := db.Exec("INSERT INTO page_images VALUES ('page',1,?,1,1)", absent); err != nil {
		t.Fatal(err)
	}
	var last string
	for i := 3; i < 7; i++ {
		stamp := now.Add(time.Duration(i) * time.Hour)
		dir, err := Create(ctx, d, db, 3, stamp)
		var incomplete *IncompleteError
		if !errors.As(err, &incomplete) || incomplete.Generation != filepath.Base(dir) || len(incomplete.Missing) != 1 || incomplete.Missing[0] != "pages/"+absent || !strings.Contains(err.Error(), absent) {
			t.Fatalf("diagnostic: %s %v", dir, err)
		}
		due, err := Due(d, "daily", stamp.Add(time.Minute))
		if err != nil || due {
			t.Fatalf("early retry %v %v", due, err)
		}
		due, err = Due(d, "daily", stamp.Add(24*time.Hour))
		if err != nil || !due {
			t.Fatalf("missed interval %v %v", due, err)
		}
		if last != "" {
			if _, err = os.Stat(last); !os.IsNotExist(err) {
				t.Fatal("older incomplete copy retained", err)
			}
		}
		last = dir
	}
	entries, err := List(ctx, d)
	if err != nil || len(entries) != 4 {
		t.Fatal(entries, err)
	}
	if !entries[0].Incomplete || len(entries[0].Missing) != 1 || entries[0].Name != filepath.Base(last) {
		t.Fatal(entries)
	}
	for _, dir := range good {
		if err := Verify(ctx, dir); err != nil {
			t.Fatal(err)
		}
	}
	var ar bytes.Buffer
	if err = ExportGeneration(ctx, last, &ar); err == nil || ar.Len() != 0 {
		t.Fatal("ordinary export accepted incomplete generation")
	}
	if err = ExportRecovery(ctx, last, &ar); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err = VerifyRecoveryArchive(ctx, dest, bytes.NewReader(ar.Bytes())); err != nil {
		t.Fatal(err)
	}
	if files, err := os.ReadDir(dest); err != nil || len(files) != 0 {
		t.Fatal("verification left staging or scratch", files, err)
	}
	if err = Stage(ctx, dest, bytes.NewReader(ar.Bytes())); err == nil {
		t.Fatal("ordinary stage accepted preservation archive")
	}
	bad := bytes.Clone(ar.Bytes())
	off := bytes.Index(bad, []byte("immutable page"))
	if off < 0 {
		t.Fatalf("archive omitted retained image %s", name)
	}
	bad[off] = '?'
	if err = VerifyRecoveryArchive(ctx, dest, bytes.NewReader(bad)); err == nil {
		t.Fatal("accepted damaged preserved image")
	}
}

type countedRead struct {
	io.ReadCloser
	read func(int)
}

func (r *countedRead) Read(p []byte) (int, error) {
	n, e := r.ReadCloser.Read(p)
	r.read(n)
	return n, e
}

func TestUpgradeReadsEachHealthyImageOnceAcrossEstimateCreationPruneAndPin(t *testing.T) {
	data, db, linked := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	first, err := Create(ctx, data, db, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	// Catch accidental reading of the live copy when a usable backup already holds the image.
	if err = os.WriteFile(filepath.Join(data, "pages", linked), []byte("corrupt live copy"), 0600); err != nil {
		t.Fatal(err)
	}
	content := []byte("a new image")
	fresh := fmt.Sprintf("%x.png", sha256.Sum256(content))
	if err = os.WriteFile(filepath.Join(data, "pages", fresh), content, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO page_images VALUES ('page',1,?,1,1)", fresh); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	realOpen := openContent
	openContent = func(path string) (io.ReadCloser, error) {
		r, err := realOpen(path)
		if err != nil {
			return nil, err
		}
		return &countedRead{r, func(n int) { counts[filepath.Base(path)] += n }}, nil
	}
	t.Cleanup(func() { openContent = realOpen })
	if _, err = Estimate(ctx, data, db, true); err != nil {
		t.Fatal(err)
	}
	if counts[linked]+counts[fresh] != 0 {
		t.Fatalf("estimate read image bytes: %v", counts)
	}
	dir, err := CreatePinned(ctx, data, db, 1, now.Add(time.Hour), "1.2", "upgrade-once")
	if err != nil {
		t.Fatal(err)
	}
	if counts[linked] != len("immutable page") || counts[fresh] != len(content) {
		t.Fatalf("read volumes: linked %d/%d, fresh %d/%d", counts[linked], len("immutable page"), counts[fresh], len(content))
	}
	if _, err = os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("pruning did not run", err)
	}
	pin := filepath.Join(data, pins, "upgrade-once", "payload")
	for _, copy := range []string{dir, pin} {
		if err := Verify(ctx, copy); err != nil {
			t.Fatal(err)
		}
	}
}

func TestImageReadFailureLeavesPriorAndPublishesNoPartial(t *testing.T) {
	data, db, name := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	first, err := Create(ctx, data, db, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	readFailure := errors.New("image read failed")
	original := openContent
	openContent = func(path string) (io.ReadCloser, error) {
		if filepath.Base(path) == name {
			return nil, readFailure
		}
		return original(path)
	}
	t.Cleanup(func() { openContent = original })
	dir, err := Create(ctx, data, db, 1, now.Add(time.Hour))
	if dir != "" || !errors.Is(err, readFailure) {
		t.Fatalf("I/O failure became an incomplete publication: %q %v", dir, err)
	}
	entries, err := List(ctx, data)
	if err != nil || len(entries) != 1 || entries[0].Name != filepath.Base(first) {
		t.Fatalf("history changed: %v %v", entries, err)
	}
}

func TestPruneRemovesIncompleteAfterRepair(t *testing.T) {
	data, db, name := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := os.Remove(filepath.Join(data, "pages", name)); err != nil {
		t.Fatal(err)
	}
	incomplete, err := CreatePinned(ctx, data, db, 1, now, "v1", "partial-pin")
	var partial *IncompleteError
	if !errors.As(err, &partial) {
		t.Fatalf("published %s: %v", incomplete, err)
	}
	if _, err = os.Lstat(filepath.Join(data, pins, "partial-pin")); !os.IsNotExist(err) {
		t.Fatal("pinned incomplete generation", err)
	}
	if err = os.WriteFile(filepath.Join(data, "pages", name), []byte("immutable page"), 0600); err != nil {
		t.Fatal(err)
	}
	var newest string
	for i := 1; i <= 2; i++ {
		newest, err = Create(ctx, data, db, 1, now.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := List(ctx, data)
	if err != nil || len(entries) != 1 || entries[0].Name != filepath.Base(newest) {
		t.Fatal(entries, err)
	}
}

func TestLatestSkipsNewerForeignDirectoryAndDoesNotScanPayload(t *testing.T) {
	d, db, _ := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, ok, err := Latest(d); err != nil || ok {
		t.Fatalf("unexpected history: %v %v", ok, err)
	}
	latest, err := Create(ctx, d, db, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(filepath.Dir(latest), generation(now.Add(time.Hour)))
	if err = os.Mkdir(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(latest, manifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(foreign, manifestFile), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	original := openContent
	openContent = func(path string) (io.ReadCloser, error) { t.Fatalf("status read %s", path); return nil, nil }
	t.Cleanup(func() { openContent = original })
	entry, ok, err := Latest(d)
	if err != nil || !ok || entry.Name != filepath.Base(latest) || entry.Incomplete || !entry.Created.Equal(now) {
		t.Fatalf("invalid latest generation: %+v %v %v", entry, ok, err)
	}
}

func TestForeignPriorImageIsNeverFollowedOrPruned(t *testing.T) {
	data, db, name := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old, err := Create(ctx, data, db, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(old, "pages", name)
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(t.TempDir(), "operator-file")
	if err = os.WriteFile(foreign, []byte("operator bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(foreign, path); err != nil {
		t.Fatal(err)
	}
	fresh, err := Create(ctx, data, db, 1, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err = Verify(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(path); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("foreign entry pruned", err)
	}
}

func TestPruningChecksHistoricalInodesOnceAndPreservesOperatorBytes(t *testing.T) {
	data, db, name := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var history []string
	for i := 0; i < 4; i++ {
		dir, err := Create(ctx, data, db, 4, now.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		history = append(history, dir)
	}
	image := func(i int) string { return filepath.Join(history[i], "pages", name) }
	good, err := os.Stat(image(0))
	if err != nil {
		t.Fatal(err)
	}
	// Same content-addressed name, byte length and mtime, but the last two
	// generations now reference unrelated operator content on a different inode.
	operator := []byte("operator bytes")
	if int64(len(operator)) != good.Size() {
		t.Fatal("invalid test fixture")
	}
	for _, i := range []int{2, 3} {
		if err = os.Remove(image(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(image(2), operator, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(image(2), good.ModTime(), good.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(image(2), image(3)); err != nil {
		t.Fatal(err)
	}
	foreign, err := os.Stat(image(2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DELETE FROM page_images"); err != nil {
		t.Fatal(err)
	}
	original := openContent
	goodBytes, foreignBytes := 0, 0
	openContent = func(path string) (io.ReadCloser, error) {
		r, err := original(path)
		if err != nil || filepath.Base(path) != name {
			return r, err
		}
		st, err := os.Stat(path)
		if err != nil {
			r.Close()
			return nil, err
		}
		return &countedRead{r, func(n int) {
			switch {
			case os.SameFile(st, good):
				goodBytes += n
			case os.SameFile(st, foreign):
				foreignBytes += n
			default:
				t.Fatal("unexpected physical image")
			}
		}}, nil
	}
	t.Cleanup(func() { openContent = original })
	if _, err = CreatePinned(ctx, data, db, 1, now.Add(5*time.Hour), "test", "pin-expired"); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{2, 3} {
		got, err := os.ReadFile(image(i))
		if err != nil || !bytes.Equal(got, operator) {
			t.Fatalf("lost foreign contents in generation %d: %q %v", i, got, err)
		}
	}
	for _, i := range []int{0, 1} {
		if _, err = os.Lstat(history[i]); !os.IsNotExist(err) {
			t.Fatal("verified expired generation survived", i, err)
		}
	}
	if goodBytes != int(good.Size()) || foreignBytes != len(operator) {
		t.Fatalf("historical bytes read: valid %d/%d, foreign %d/%d", goodBytes, good.Size(), foreignBytes, len(operator))
	}
}

func TestOperationChecksumDoesNotTrustChangedInodeContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	checks := make(imageChecks)
	first, err := checks.checksum(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := st.ModTime().Add(time.Minute)
	if err = os.Chtimes(path, changed, changed); err != nil {
		t.Fatal(err)
	}
	actual, err := checks.checksum(path)
	expected := fmt.Sprintf("%x", sha256.Sum256([]byte("other")))
	if err != nil || actual == first || actual != expected {
		t.Fatalf("cached stale contents: actual=%s expected=%s err=%v", actual, expected, err)
	}
}

func TestPruningReusesHashOfRestoredLiveImage(t *testing.T) {
	data, db, name := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old, err := Create(ctx, data, db, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DELETE FROM page_images"); err != nil {
		t.Fatal(err)
	}
	if _, err = Create(ctx, data, db, 2, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Local restore links the live image to its historical version. The latest
	// generation doesn't reference it, so creation must populate a private copy.
	live := filepath.Join(data, "pages", name)
	if err = os.Remove(live); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(filepath.Join(old, "pages", name), live); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO page_images VALUES ('page',0,?,1,1)", name); err != nil {
		t.Fatal(err)
	}
	original := openContent
	n := 0
	openContent = func(path string) (io.ReadCloser, error) {
		r, e := original(path)
		if e != nil || filepath.Base(path) != name {
			return r, e
		}
		return &countedRead{r, func(amount int) { n += amount }}, nil
	}
	t.Cleanup(func() { openContent = original })
	if _, err = CreatePinned(ctx, data, db, 1, now.Add(2*time.Hour), "test", "restored-image"); err != nil {
		t.Fatal(err)
	}
	if n != len("immutable page") {
		t.Fatalf("read %d bytes from restored image; want %d", n, len("immutable page"))
	}
}

func TestNewReferencesSharingLiveInodeNeedOneRead(t *testing.T) {
	data, db, name := fixture(t)
	ctx := context.Background()
	alias := strings.TrimSuffix(name, ".png") + ".jpg"
	if err := os.Link(filepath.Join(data, "pages", name), filepath.Join(data, "pages", alias)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO page_images VALUES ('page',1,?,1,1)", alias); err != nil {
		t.Fatal(err)
	}
	original := openContent
	total := 0
	openContent = func(path string) (io.ReadCloser, error) {
		r, e := original(path)
		if e != nil || (filepath.Base(path) != name && filepath.Base(path) != alias) {
			return r, e
		}
		return &countedRead{r, func(n int) { total += n }}, nil
	}
	t.Cleanup(func() { openContent = original })
	if _, err := CreatePinned(ctx, data, db, 1, time.Now(), "test", "two-names"); err != nil {
		t.Fatal(err)
	}
	if total != len("immutable page") {
		t.Fatalf("read %d image bytes, wanted 14", total)
	}
}
