package live

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pjerra/forever-agent/internal/slots"
)

// ServerExt is the optional per-server `forever` block of a slots file used
// by the trials runner: the rest/proxy knobs of a live slot plus where the
// runner finds its serve dirs. It carries no secrets.
type ServerExt struct {
	Rest         string `json:"rest"`
	RPC          string `json:"rpc"`
	CA           string `json:"ca"`
	Build        string `json:"build"`
	ServeDirRoot string `json:"serve_dir_root"`
}

// SlotsFile is a slots file plus the runner's optional per-server `forever`
// blocks. The blocks live beside `servers`, keyed by server name; the stdlib
// decoder leaves unknown fields alone, so an A3 file with more keys keeps
// loading.
type SlotsFile struct {
	Servers []slots.Server       `json:"servers"`
	Forever map[string]ServerExt `json:"forever"`
}

// LoadSlotsFile reads path like slots.Load (including the 0600 dir/file
// permission checks) and also decodes the optional `forever` block.
func LoadSlotsFile(path string) (*SlotsFile, error) {
	cfg, err := slots.Load(path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Forever map[string]ServerExt `json:"forever"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	_ = dec.Decode(&doc)
	return &SlotsFile{Servers: cfg.Servers, Forever: doc.Forever}, nil
}

// Hold is one leased slot: the acquired record, its store (for renew and
// release), and the serve dir the runner opened under the server's
// serve_dir_root.
type Hold struct {
	Server string
	Slot   string
	Acq    slots.Acquired
	Store  *slots.Store
	Dir    string
}

// leaseTTL is the lease lifetime at acquire; RenewEvery is the renewal
// interval while a trial holds the slot.
const (
	leaseTTL   = 30 * time.Minute
	RenewEvery = 60 * time.Second
)

// AcquireSlot leases server/slotID (or any free slot) for holder and opens
// <serve_dir_root>/<slot> with openSlot. serve_dir_root comes from the
// slots file's `forever` block; without it there is no serve to talk to.
func AcquireSlot(sf *SlotsFile, store *slots.Store, server, slotID string, any bool, holder string, openSlot func(dir string) error) (*Hold, error) {
	ext := sf.Forever[server]
	if ext.ServeDirRoot == "" {
		return nil, fmt.Errorf("slot %s: no serve running (server %q has no forever.serve_dir_root)", slotID, server)
	}
	acq, err := store.Acquire(server, slotID, any, holder, leaseTTL)
	if err != nil {
		return nil, err
	}
	slot := acq.Slot.ID
	dir := filepath.Join(ext.ServeDirRoot, slot)
	if err := openSlot(dir); err != nil {
		_, _ = store.Release(server, slot, holder, acq.Lease.LeaseID, false)
		return nil, fmt.Errorf("slot %s: no serve running (%v)", slot, err)
	}
	return &Hold{Server: server, Slot: slot, Acq: acq, Store: store, Dir: dir}, nil
}

// RenewOnce extends h's lease by the acquire TTL.
func (h *Hold) RenewOnce() error {
	l, err := h.Store.Renew(h.Server, h.Slot, h.Acq.Lease.Holder, h.Acq.Lease.LeaseID, leaseTTL)
	if err != nil {
		return err
	}
	h.Acq.Lease = l
	return nil
}

// Release frees h's lease.
func (h *Hold) Release() error {
	_, err := h.Store.Release(h.Server, h.Slot, h.Acq.Lease.Holder, h.Acq.Lease.LeaseID, false)
	return err
}

// KeepRenewed renews h every RenewEvery until done closes; the first error
// (or a done close) stops it. The caller defers Release separately.
func (h *Hold) KeepRenewed(done <-chan struct{}) {
	t := time.NewTicker(RenewEvery)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if err := h.RenewOnce(); err != nil {
				return
			}
		}
	}
}
