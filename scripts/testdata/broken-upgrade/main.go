// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Test image entrypoint. Make a forward-only schema change, then fail before serving status.
package main

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

// Leave free space between a two-copy and a three-copy upgrade budget on the
// limited tmpfs. Use SQLite's actual page count and measure allocated bytes.
func setUpgradeHeadroom(accept bool) error {
	db, err := sql.Open("sqlite", "/data/abyssreader.db")
	if err != nil {
		return err
	}
	defer db.Close()
	var size int64
	if err := db.QueryRow("SELECT page_size * page_count FROM pragma_page_size, pragma_page_count").Scan(&size); err != nil {
		return err
	}
	available := func() (int64, error) {
		var fs syscall.Statfs_t
		err := syscall.Statfs("/data", &fs)
		return int64(fs.Bavail) * int64(fs.Bsize), err
	}
	file, err := os.Create("/data/upgrade-pressure")
	if err != nil {
		return err
	}
	defer file.Close()
	before, err := available()
	if err != nil {
		return err
	}
	lower := int64(64<<20) + 2*size
	upper := lower + size
	if accept {
		lower = upper
		upper += size / 4
	}
	block := make([]byte, 1<<20)
	for left := before - (lower+upper)/2; left > 0; {
		n, err := file.Write(block[:min(left, int64(len(block)))])
		if err != nil {
			return err
		}
		left -= int64(n)
	}
	if err := file.Sync(); err != nil {
		return err
	}
	free, err := available()
	if err != nil {
		return err
	}
	if free <= lower || free >= upper {
		return fmt.Errorf("invalid fixture: need %d < free (%d) < %d", lower, free, upper)
	}
	return nil
}

// Seed real rows covered by migrations 7/8 before capturing any baseline.
func seedSync() error {
	db, err := sql.Open("sqlite", "/data/abyssreader.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`BEGIN IMMEDIATE;
INSERT INTO users (email,email_key,display_name,created_at,updated_at)
VALUES ('backup-test@example.org','backup-test@example.org','Archive fixture',1,1);
INSERT INTO sync_accounts (user_id,seq) VALUES (last_insert_rowid(),1);
INSERT INTO sync_records (user_id,kind,key_title,source_slug,chapter_url,url_hash,title_id,client_updated_at,deleted,seq)
SELECT u.id,'favorite',t.id,'','',x'',t.id,1,0,1 FROM users u,titles t WHERE u.email_key='backup-test@example.org' ORDER BY t.id LIMIT 1;
COMMIT;`)
	return err
}

func alterBackupCopies(action, rel string) error {
	if filepath.Dir(rel) != "covers" || strings.ContainsAny(rel, "*?[]") || strings.Contains(rel, "..") {
		return fmt.Errorf("invalid relative cover path")
	}
	generations, err := filepath.Glob("/data/backups/kit-*")
	if err != nil || len(generations) < 1 {
		return fmt.Errorf("no fixture generation found: %v", err)
	}
	if action == "damage-newest" {
		if len(generations) < 2 {
			return fmt.Errorf("need an intact older generation")
		}
		path := filepath.Join(generations[len(generations)-1], rel)
		// Replace the name, never mutate an inode shared with older generations.
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("damaged newest backup"), 0600)
	}
	// This case runs on a newly installed disposable volume with no rollback
	// pins. Keep the external golden archive while removing every local copy.
	for _, dir := range append(generations, "/data") {
		if err := os.Remove(filepath.Join(dir, rel)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func truncateDatabase() error {
	db := "/data/abyssreader.db"
	if err := os.Truncate(db, 73); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.WriteFile(db+suffix, []byte("interrupted SQLite fixture "+suffix), 0600); err != nil {
			return err
		}
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		b, err := os.ReadFile(db + suffix)
		if err != nil {
			return err
		}
		fmt.Printf("%x  %s\n", sha256.Sum256(b), filepath.Base(db+suffix))
	}
	return nil
}

func seedBudget() error {
	db, err := sql.Open("sqlite", "/data/abyssreader.db?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec("CREATE TABLE e2e_ballast(payload BLOB NOT NULL); INSERT INTO e2e_ballast VALUES (zeroblob(4194304)); PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

func outputNote() error {
	paths, err := filepath.Glob("/data/.kit-abandoned-*/abandon.json")
	if err != nil || len(paths) != 1 {
		return fmt.Errorf("expected one abandonment note, got %v (%v)", paths, err)
	}
	body, err := os.ReadFile(paths[0])
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(body)
	return err
}

func fixtureCommand(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "hold":
		for {
			time.Sleep(time.Hour)
		}
	case "seed-sync":
		return true, seedSync()
	case "seed-budget":
		return true, seedBudget()
	case "damage-newest", "lose-every-copy":
		if len(args) != 2 {
			return true, fmt.Errorf("%s needs an image path", args[0])
		}
		return true, alterBackupCopies(args[0], args[1])
	case "truncate-db":
		return true, truncateDatabase()
	case "abandon-note":
		return true, outputNote()
	default:
		return false, nil
	}
}

func main() {
	if handled, err := fixtureCommand(os.Args[1:]); handled {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "check-abandoned" {
		files, err := filepath.Glob("/data/.kit-abandoned-*/transaction/new/abyssreader.db")
		if err != nil || len(files) != 1 {
			log.Fatalf("lost the staged restore database: %v (%v)", files, err)
		}
		for _, path := range []string{"/data/.kit-operation", "/data/.kit-restore"} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				log.Fatal("guard still active at ", path)
			}
		}
		return
	}
	if len(os.Args) == 2 && (os.Args[1] == "upgrade-headroom" || os.Args[1] == "accepted-headroom") {
		if err := setUpgradeHeadroom(os.Args[1] == "accepted-headroom"); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) == 4 && os.Args[1] == "damage" {
		// Unlink live names FIRST so even a hard-linked older snapshot stays healthy.
		for i, dir := range []string{"covers", "pages"} {
			rel := os.Args[i+2]
			if filepath.Dir(rel) != dir || strings.Contains(rel, "..") {
				log.Fatal("expected image in ", dir)
			}
			if err := os.Remove(filepath.Join("/data", rel)); err != nil {
				log.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join("/data", os.Args[3]), []byte("changed content"), 0600); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "fill" {
		file, err := os.Create("/data/pressure")
		if err != nil {
			log.Fatal(err)
		}
		block := make([]byte, 1<<20)
		for range 12 {
			if _, err := file.Write(block); err != nil {
				log.Fatal(err)
			}
		}
		if err := file.Sync(); err != nil {
			log.Fatal(err)
		}
		if err := file.Close(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] != "serve" {
		server := "/usr/local/bin/abyssreader-server"
		log.Fatal(syscall.Exec(server, append([]string{server}, os.Args[1:]...), os.Environ()))
	}
	db, err := sql.Open("sqlite", "/data/abyssreader.db")
	if err != nil {
		log.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE IF NOT EXISTS incompatible_upgrade(x); DELETE FROM chapters"); err != nil {
		log.Fatal(err)
	}
	var ballast int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name='e2e_ballast'").Scan(&ballast); err != nil {
		log.Fatal(err)
	}
	if ballast == 1 {
		if _, err := db.Exec("CREATE TABLE failed_migration_copy AS SELECT * FROM e2e_ballast"); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile("/data/e2e-migration-copied", []byte("4194304"), 0600); err != nil {
			log.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		log.Fatal(err)
	}
	if err = os.WriteFile("/data/test-migration-ran", nil, 0600); err != nil {
		log.Fatal(err)
	}
	os.Exit(1)
}
