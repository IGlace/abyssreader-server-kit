// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type operationRecord struct {
	Kind      string `json:"kind"`
	Supported int64  `json:"supportedSchema"`
	Pin       string `json:"pin,omitempty"`
}

func (r operationRecord) valid() bool {
	switch r.Kind {
	case "upgrade", "restore", "rollback", "uninstall":
	default:
		return false
	}
	schemaKnown := r.Supported > 0
	legacyPin := r.Supported == 0 && ((r.Pin != "" && r.Kind == "restore") || r.Kind == "uninstall")
	return (schemaKnown || legacyPin) && (r.Pin == "" || pinID.MatchString(r.Pin))
}

func readRecordFile(path string) ([]byte, error) {
	if err := checkType(path, false); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if len(b) > 4096 {
		return nil, errors.New("oversized recovery metadata")
	}
	return b, err
}

func operationDetails(data string) (operationRecord, error) {
	var r operationRecord
	raw, err := readRecordFile(filepath.Join(data, operation))
	if os.IsNotExist(err) || (err == nil && len(raw) == 0) {
		// Releases before versioned operation metadata left an empty boot interlock.
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if json.Unmarshal(raw, &r) != nil || !r.valid() {
		return operationRecord{}, errors.New("invalid operation metadata")
	}
	return r, nil
}

// Publish the complete metadata together with the interlock, before the host can change
// files or start a migration. A retry may reuse only the same recorded intent.
func RecordOperation(data, kind string, supported int64, pin string) error {
	r := operationRecord{kind, supported, pin}
	if !r.valid() {
		return errors.New("invalid operation kind, schema or pin")
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(data, ".kit-arming-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(encoded); err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err = os.Link(f.Name(), filepath.Join(data, operation)); os.IsExist(err) {
		have, readErr := operationDetails(data)
		if readErr != nil {
			return readErr
		}
		if have != r {
			return errors.New("another operation already owns the data volume")
		}
	} else if err != nil {
		return err
	}
	return syncPath(data)
}

func (r operationRecord) label() string {
	if r.Kind == "" {
		return "unknown (legacy marker)"
	}
	return r.Kind
}

func (r operationRecord) pinPath() string {
	if r.Pin == "" {
		return "none recorded"
	}
	return "/data/" + pins + "/" + r.Pin
}

func (r operationRecord) consequence() string {
	switch r.Kind {
	case "upgrade":
		return "Abandoning this upgrade keeps the verified database, including any applied migration. Reinstall a compatible image or restore the pre-upgrade pin."
	case "restore":
		return "Abandoning this restore keeps a verified completed swap, or reassembles the original files if the swap was interrupted."
	case "rollback":
		return "Abandoning this rollback keeps a verified completed rollback, or reassembles the pre-rollback files if the swap was interrupted."
	case "uninstall":
		return "Abandoning this uninstall keeps the remaining volume data and cancels any pending deletion."
	default:
		return "The older interlock did not record the operation kind. Abandoning it keeps a verified database and preserves displaced files."
	}
}

type abandonment struct {
	Operation operationRecord `json:"operation"`
	Applied   int64           `json:"appliedSchema"`
	At        time.Time       `json:"abandonedAt"`
	PinPath   string          `json:"pinPath"`
}

func (a abandonment) summary() string {
	return fmt.Sprintf("Last abandonment %s: %s, applied schema %d, rollback pin %s.", a.At.UTC().Format(time.RFC3339Nano), a.Operation.label(), a.Applied, a.Operation.pinPath())
}

func LastAbandonment(data string) (string, error) {
	entries, err := os.ReadDir(data)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var last abandonment
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ".kit-abandoned-") {
			continue
		}
		raw, err := readRecordFile(filepath.Join(data, e.Name(), "abandon.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		var a abandonment
		if json.Unmarshal(raw, &a) != nil || a.At.IsZero() || a.Applied < 1 || (a.Operation != (operationRecord{}) && !a.Operation.valid()) {
			return "", errors.New("invalid abandonment audit note")
		}
		if a.At.After(last.At) {
			last = a
		}
	}
	if last.At.IsZero() {
		return "", nil
	}
	return last.summary(), nil
}

func describeOperation(ctx context.Context, data string, r operationRecord) ([]string, error) {
	supported := "unknown (legacy marker)"
	if r.Supported > 0 {
		supported = fmt.Sprint(r.Supported)
	}
	newest := "none"
	entries, err := List(ctx, data)
	if err != nil {
		return nil, err
	}
	for _, g := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !g.Raw && !g.Incomplete && Verify(ctx, filepath.Join(data, "backups", g.Name)) == nil {
			newest = "/data/backups/" + g.Name + "/backup.json"
			break
		}
	}
	if r.Kind != "" && r.Supported == 0 {
		supported = "unknown (pin has no image schema receipt)"
		if r.Pin == "" {
			supported = "unknown (installed image did not report its schema)"
		}
	}
	lines := []string{"Operation: " + r.label(), "Pinned image supports schema: " + supported, "Rollback pin: " + r.pinPath(), "Newest complete generation: " + newest, r.consequence()}
	if r.Pin != "" {
		dir, err := pinDirectory(data, r.Pin)
		if err == nil {
			_, err = pinSource(dir)
		}
		if err != nil {
			if r.Kind == "upgrade" {
				lines = append(lines, "The planned pre-upgrade pin is not available on the volume.")
			} else {
				lines = append(lines, "The recorded rollback pin is not available on the volume.")
			}
		}
	}
	return lines, nil
}

// RecordUnlockedAbandonment audits explicit abandonment of an unreadable host journal
// whose volume interlock has already been cleared. It never creates a database.
func RecordUnlockedAbandonment(ctx context.Context, data string) error {
	if err := Ready(data); err != nil {
		return err
	}
	schema, err := liveSchema(ctx, data)
	if err != nil {
		return err
	}
	_, err = writeAbandonment(data, operationRecord{}, schema)
	return err
}

// Pins created before schema receipts existed return zero. The installed image may have
// advanced since the pin was created, so its schema limit is not a substitute.
func PinImageSchema(data, id string) (int64, error) {
	dir, err := pinDirectory(data, id)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if _, err = pinSource(dir); os.IsNotExist(err) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	raw, err := readRecordFile(filepath.Join(dir, "image-schema"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	schema, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || schema < 1 {
		return 0, errors.New("invalid pin image schema receipt")
	}
	return schema, nil
}
