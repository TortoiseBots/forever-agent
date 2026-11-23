package minimap

// Dungeon interiors from the CASC install: a WMO-only map's WDT names the map's root WMO file by FileDataID, and the
// client's WMOMinimapTexture table gives one minimap block texture per (group, BlockX, BlockY) of the WMO root's
// MOGI grid. Each block is placed at the group box's minimum plus the block index times the client's block size, which
// is the one value the client data does not state outright: WMOBlockYards below was measured from the map's own
// geometry with `minimap-extract --wmo-probe` (the probe prints the range each group implies).

import (
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"sort"
	"strconv"

	"github.com/pjerra/forever-agent/internal/casc"
	"github.com/pjerra/forever-agent/internal/db2"
	"github.com/pjerra/forever-agent/internal/mapview"
)

// WMOBlockYards is the side of one WMOMinimapTexture block in yards, the open value of round 4. It was measured from
// the client install on 2026-11-23 by comparing each group's MOGI extent with the highest BlockX/BlockY the
// WMOMinimapTexture rows reach: a group of E yards covered by n blocks spans E/n <= block < E/(n-1), and the
// intersection over the groups of the Ragefire Chasm and the Wailing Caverns interiors is (127.08, 128.76] yd, so
// 128. The block textures agree: they are clipped to the group box and all drawn at 2 px a yard (256x256 for a full
// block, 256x128 and 64x256 at the edges). The probe reports the range, and the missing --wmo-block-yards error
// repeats the value, so the flag is never a guess.
const WMOBlockYards = 128

// Reasons a CASC-mode map has no WMO interior picture (the status file's wording).
const (
	cascNoWMORoot   = "no minimap picture: the client's WDT names no WMO root file (MODF/MWID)"
	cascNoWMOBlocks = "no minimap picture: WMOMinimapTexture has no blocks for this WMO"
	cascNoWMOBlock  = "no minimap picture: none of the WMO's block textures is installed"
	cascNoWMOTable  = "no minimap picture: WMOMinimapTexture.db2 is not in the --db2 folder (the server's map extractor writes it)"
)

// wmoCascMaxBlocks bounds the block textures one map reads, so a hostile table cannot make the run read forever.
const wmoCascMaxBlocks = 16384

// wmoCascMissingBlockYards is the error of a WMO composition that was given no block size. It names the probe, which
// prints the measured value, so the caller is never left with a silent default.
var wmoCascMissingBlockYards = errors.New("the WMO interior picture needs --wmo-block-yards: the client's block size is " +
	strconv.FormatFloat(WMOBlockYards, 'g', -1, 64) + " yd, measured with `minimap-extract --wmo-probe`")

// wdtMODF is one WDT MODF entry: the WMO placement of a WMO-only map. Flags bit 0x8 says Id is the root file's
// FileDataID instead of the index into MWMO/MWID.
type wdtMODF struct {
	nameID uint32
	uid    uint32
	flags  uint16
	pos    [3]float64
	rot    [3]float64
}

// wdtWMO is what a WDT says about the WMO an indoor map is made of: the MPHD flags, the MWMO names, the MWID
// FileDataIDs and the MODF placements.
type wdtWMO struct {
	flags uint32
	names []string
	ids   []uint32
	modfs []wdtMODF
}

// parseWDTWMO reads those four chunks of a WDT. A truncated chunk ends the walk (walkChunks' rule).
func parseWDTWMO(wdt []byte) wdtWMO {
	var w wdtWMO
	var mwmo, mwid, modf []byte
	walkChunks(wdt, func(id string, d []byte) {
		switch id {
		case "MPHD":
			if len(d) >= 4 {
				w.flags = le32(d, 0)
			}
		case "MWMO":
			mwmo = append(mwmo, d...)
		case "MWID":
			mwid = append(mwid, d...)
		case "MODF":
			modf = append(modf, d...)
		}
	})
	w.names = splitCStrings(mwmo)
	for o := 0; o+4 <= len(mwid); o += 4 {
		w.ids = append(w.ids, le32(mwid, o))
	}
	for o := 0; o+64 <= len(modf); o += 64 {
		e := modf[o : o+64]
		m := wdtMODF{nameID: le32(e, 0), uid: le32(e, 4), flags: uint16(le32(e, 56))}
		for k := range 3 {
			m.pos[k], m.rot[k] = f32(e, 8+4*k), f32(e, 20+4*k)
		}
		if !saneMODF(m) {
			continue
		}
		w.modfs = append(w.modfs, m)
	}
	return w
}

// saneMODF rejects a placement with non-finite or absurd numbers, as parseMODF does.
func saneMODF(m wdtMODF) bool {
	for _, v := range []float64{m.pos[0], m.pos[1], m.pos[2], m.rot[0], m.rot[1], m.rot[2]} {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1e6 {
			return false
		}
	}
	return true
}

// rootID is the root WMO's FileDataID: the MODF entry whose Id the client stores as a FileDataID (flags 0x8), else
// the MWID entry that Id indexes.
func (w wdtWMO) rootID() (uint32, bool) {
	for _, m := range w.modfs {
		if m.flags&0x8 != 0 && m.nameID != 0 {
			return m.nameID, true
		}
		if int(m.nameID) < len(w.ids) && w.ids[m.nameID] != 0 {
			return w.ids[m.nameID], true
		}
	}
	return 0, false
}

// parseMOHD reads the root WMO's ID (MOHD's RootWMOID field, byte 32 of the chunk): WMOMinimapTexture's rows are
// keyed by that id, not by the root file's FileDataID.
func parseMOHD(root []byte) (uint32, bool) {
	var id uint32
	ok := false
	walkChunks(root, func(c string, d []byte) {
		if c == "MOHD" && len(d) >= 36 && !ok {
			id, ok = le32(d, 32), true
		}
	})
	return id, ok
}

// parseMOGI reads the group boxes of a WMO root. Group ids are positional; a rejected box keeps its slot (nil) so a
// block's GroupNum still names its own group.
func parseMOGI(root []byte) []*wmoGroup {
	var mogi []byte
	walkChunks(root, func(id string, d []byte) {
		if id == "MOGI" {
			mogi = append(mogi, d...)
		}
	})
	n := min(len(mogi)/32, wmoMaxGroups)
	groups := make([]*wmoGroup, n)
	for i := range n {
		o := i * 32
		g := &wmoGroup{index: i, flags: le32(mogi, o),
			x0: f32(mogi, o+4), y0: f32(mogi, o+8), z0: f32(mogi, o+12),
			x1: f32(mogi, o+16), y1: f32(mogi, o+20), z1: f32(mogi, o+24)}
		if !saneBox(g) {
			continue
		}
		groups[i] = g
	}
	return groups
}

// wmoCASCFile is a map's root WMO: its FileDataID, the MOHD id the block table keys on, and the MOGI group boxes.
type wmoCASCFile struct {
	root   uint32
	wmoID  uint32
	groups []*wmoGroup
}

// openWMOCASC reads a map's WDT by FileDataID and its root WMO's MOGI boxes. ok is false with a one-line reason when
// the client names no root WMO; a broken WDT or WMO file is an error.
func openWMOCASC(src fileSource, wdtID int32, logf func(string, ...any)) (*wmoCASCFile, bool, string, error) {
	if wdtID <= 0 {
		return nil, false, cascNoWDT, nil
	}
	wdt, err := src.ReadFileDataID(uint32(wdtID))
	if err != nil {
		if errors.Is(err, casc.ErrNotFound) {
			return nil, false, fmt.Sprintf("no minimap picture: the WDT FileDataID %d is not installed", wdtID), nil
		}
		return nil, false, "", fmt.Errorf("WDT FileDataID %d: %w", wdtID, err)
	}
	w := parseWDTWMO(wdt)
	root, ok := w.rootID()
	if !ok {
		return &wmoCASCFile{}, false, cascNoWMORoot, nil
	}
	rb, err := src.ReadFileDataID(root)
	if err != nil {
		if errors.Is(err, casc.ErrNotFound) {
			return &wmoCASCFile{root: root}, false,
				fmt.Sprintf("no minimap picture: the root WMO FileDataID %d is not installed", root), nil
		}
		return nil, false, "", fmt.Errorf("root WMO FileDataID %d: %w", root, err)
	}
	wmoID, hasMOHD := parseMOHD(rb)
	if !hasMOHD {
		return &wmoCASCFile{root: root}, false,
			fmt.Sprintf("no minimap picture: the root WMO FileDataID %d has no MOHD header", root), nil
	}
	groups := parseMOGI(rb)
	if len(groups) == 0 {
		return &wmoCASCFile{root: root, wmoID: wmoID}, false,
			fmt.Sprintf("no minimap picture: the root WMO FileDataID %d has no MOGI groups", root), nil
	}
	logf("WDT %d: root WMO FileDataID %d (MOHD id %d), %d groups", wdtID, root, wmoID, len(groups))
	return &wmoCASCFile{root: root, wmoID: wmoID, groups: groups}, true, "", nil
}

// wmoCascBlock is one placed block texture: its group, its block indices, its group-local rectangle in yards and its
// decoded picture.
type wmoCascBlock struct {
	img *image.NRGBA
	x0  float64
	y0  float64
	x1  float64
	y1  float64
}

// rect is the block's output rectangle on a canvas of m (north up, west left): world north = -x, west = -y.
func (b wmoCascBlock) rect(m Meta) image.Rectangle {
	pc0, pr0 := m.ToPixel(-b.x0, -b.y0)
	pc1, pr1 := m.ToPixel(-b.x1, -b.y1)
	r := image.Rect(int(math.Floor(pc0)), int(math.Floor(pr0)), int(math.Ceil(pc1)), int(math.Ceil(pr1)))
	return r.Intersect(image.Rect(0, 0, m.Width, m.Height))
}

// draw paints the block's output rectangle. As in wmoJob.draw, the picture's columns run with the group's x and its
// rows (counted from the bottom) with the group's y; a pixel is drawn only when it is opaque (alpha >= 128).
func (b wmoCascBlock) draw(img *image.NRGBA, m Meta) {
	bb := b.img.Bounds()
	w, h := bb.Dx(), bb.Dy()
	rect := b.rect(m)
	for r := rect.Min.Y; r < rect.Max.Y; r++ {
		for c := rect.Min.X; c < rect.Max.X; c++ {
			X, Y := m.ToWorld(float64(c)+.5, float64(r)+.5)
			x, y := -X, -Y
			if x < b.x0 || x > b.x1 || y < b.y0 || y > b.y1 {
				continue
			}
			// The block's own pixels: its rectangle's origin is the picture's (u, v) origin.
			u := int((x - b.x0) * wmoPxPerYd)
			v := h - 1 - int((y-b.y0)*wmoPxPerYd)
			if u < 0 || v < 0 || u >= w || v >= h {
				continue
			}
			px := b.img.NRGBAAt(u, v)
			if px.A < 128 {
				continue
			}
			px.A = 255
			img.SetNRGBA(c, r, px)
		}
	}
}

// wmoFloors splits the groups that have blocks into floors by their MOGI boxes' heights: the mapview floor gap
// separates groups whose boxes do not meet, the same cut the navmesh floors use. Each floor is a list of group ids,
// bottom up.
func wmoFloors(groups []*wmoGroup, withBlocks map[int]bool) [][]int {
	var order []int
	for i, g := range groups {
		if g != nil && withBlocks[i] {
			order = append(order, i)
		}
	}
	sort.Slice(order, func(a, b int) bool {
		ga, gb := groups[order[a]], groups[order[b]]
		if ga.z0 != gb.z0 {
			return ga.z0 < gb.z0
		}
		return order[a] < order[b]
	})
	gap := float64(mapview.DefaultFloorOptions().MinGap)
	var floors [][]int
	var top float64
	for _, i := range order {
		g := groups[i]
		if len(floors) == 0 || g.z0 > top+gap {
			floors = append(floors, []int{i})
			top = g.z1
			continue
		}
		floors[len(floors)-1] = append(floors[len(floors)-1], i)
		top = math.Max(top, g.z1)
	}
	return floors
}

// wmoFromCASC composes a WMO-only map's minimap from the client's block textures: one layer per floor, the lowest
// first. ok is false with a one-line reason when the client gives no picture; a missing number is an error naming the
// probe.
func wmoFromCASC(src fileSource, blocks []db2.WMOMinimapTexture, wdtID int32, blockYards float64, logf func(string, ...any)) ([]Layer, bool, string, error) {
	f, ok, reason, err := openWMOCASC(src, wdtID, logf)
	if err != nil || !ok {
		return nil, false, reason, err
	}
	if blockYards <= 0 {
		return nil, false, "", fmt.Errorf("map WDT %d: %w", wdtID, wmoCascMissingBlockYards)
	}
	if blocks == nil {
		return nil, false, cascNoWMOTable, nil
	}
	rows := make([]db2.WMOMinimapTexture, 0, len(blocks))
	for _, b := range blocks {
		if b.WMOID == f.wmoID {
			rows = append(rows, b)
		}
	}
	if len(rows) == 0 {
		return nil, false, fmt.Sprintf("%s (MOHD id %d)", cascNoWMOBlocks, f.wmoID), nil
	}
	placed := map[int][]db2.WMOMinimapTexture{}
	withBlocks := map[int]bool{}
	for _, b := range rows {
		if int(b.GroupNum) >= len(f.groups) || f.groups[b.GroupNum] == nil {
			logf("map %d: WMOMinimapTexture block for group %d of WMO %d: the WMO has no such group; skipped", wdtID, b.GroupNum, f.root)
			continue
		}
		placed[int(b.GroupNum)] = append(placed[int(b.GroupNum)], b)
		withBlocks[int(b.GroupNum)] = true
	}
	if len(placed) == 0 {
		return nil, false, fmt.Sprintf("%s (FileDataID %d)", cascNoWMOBlocks, f.root), nil
	}
	floors := wmoFloors(f.groups, withBlocks)
	var out []Layer
	var reasons []string
	for i, floor := range floors {
		suffix := ""
		if i > 0 {
			suffix = "-" + strconv.Itoa(i+1) + "f"
		}
		l, ok, reason, err := composeWMOFloor(src, f.groups, placed, floor, blockYards, suffix, wdtID, logf)
		if err != nil {
			return nil, false, "", err
		}
		if !ok {
			reasons = append(reasons, reason)
			continue
		}
		out = append(out, *l)
	}
	if len(out) == 0 {
		reason := cascNoWMOBlock
		if len(reasons) > 0 {
			reason = reasons[0]
		}
		return nil, false, reason, nil
	}
	return out, true, "", nil
}

// composeWMOFloor composes one floor's picture: the block textures of the floor's groups, placed on one canvas.
func composeWMOFloor(src fileSource, groups []*wmoGroup, placed map[int][]db2.WMOMinimapTexture, floor []int, blockYards float64, suffix string, wdtID int32, logf func(string, ...any)) (*Layer, bool, string, error) {
	var list []wmoCascBlock
	read := 0
	for _, gi := range floor {
		g := groups[gi]
		rows := placed[gi]
		sort.Slice(rows, func(a, b int) bool {
			if rows[a].BlockX != rows[b].BlockX {
				return rows[a].BlockX < rows[b].BlockX
			}
			return rows[a].BlockY < rows[b].BlockY
		})
		seen := map[[2]int]int32{}
		duplicates := 0
		for _, b := range rows {
			k := [2]int{int(b.BlockX), int(b.BlockY)}
			if prev, dup := seen[k]; dup {
				// The client's table repeats a block's row verbatim (one big WMO's group lists the same block 30
				// times); an identical repeat is the same block, not a conflict.
				if prev == b.FileDataID {
					duplicates++
					continue
				}
				return nil, false, "", fmt.Errorf("map WDT %d: WMOMinimapTexture has two blocks for group %d at %d,%d (%d and %d)",
					wdtID, gi, b.BlockX, b.BlockY, prev, b.FileDataID)
			}
			seen[k] = b.FileDataID
			if b.FileDataID <= 0 {
				continue
			}
			read++
			if read > wmoCascMaxBlocks {
				return nil, false, "", fmt.Errorf("map WDT %d: the WMO's blocks are more than %d", wdtID, wmoCascMaxBlocks)
			}
			data, err := src.ReadFileDataID(uint32(b.FileDataID))
			if errors.Is(err, casc.ErrNotFound) {
				logf("map %d: WMO block FileDataID %d is not installed", wdtID, b.FileDataID)
				continue
			}
			if err != nil {
				return nil, false, "", fmt.Errorf("WMO block FileDataID %d: %w", b.FileDataID, err)
			}
			img, err := decodeWMOTile(data)
			if err != nil {
				logf("map %d: WMO block FileDataID %d: %v", wdtID, b.FileDataID, err)
				continue
			}
			// A block is the client's 128-yard square, and the texture is the tile of the group's minimap image at
			// wmoPxPerYd: 256x256 for a full block, 256x128 or 64x256 where the image's next-power-of-two edge crops
			// it. Its own pixels therefore give how far the art reaches inside the block.
			bb := img.Bounds()
			x0 := g.x0 + float64(b.BlockX)*blockYards
			y0 := g.y0 + float64(b.BlockY)*blockYards
			x1 := x0 + math.Min(float64(bb.Dx())/wmoPxPerYd, blockYards)
			y1 := y0 + math.Min(float64(bb.Dy())/wmoPxPerYd, blockYards)
			if x1 <= x0 || y1 <= y0 {
				logf("map %d: WMO group %d block %d,%d has no pixels; skipped", wdtID, gi, b.BlockX, b.BlockY)
				continue
			}
			list = append(list, wmoCascBlock{img: img, x0: x0, y0: y0, x1: x1, y1: y1})
		}
	}
	if len(list) == 0 {
		return nil, false, cascNoWMOBlock, nil
	}
	m := Meta{Source: KindMinimap, From: fromWMO}
	N, S, W, E := -1e18, 1e18, -1e18, 1e18
	for _, b := range list {
		N, S = math.Max(N, -b.x0), math.Min(S, -b.x1)
		W, E = math.Max(W, -b.y0), math.Min(E, -b.y1)
	}
	m.North, m.South, m.West, m.East = N, S, W, E
	if m.West <= m.East || m.North <= m.South {
		return nil, false, "the WMO's blocks have no extent", nil
	}
	ppy := wmoPxPerYd
	if v := wmoMaxSide / (m.West - m.East); v < ppy {
		ppy = v
	}
	if v := wmoMaxSide / (m.North - m.South); v < ppy {
		ppy = v
	}
	if ppy <= 0 {
		return nil, false, "the WMO's blocks are too wide for a picture", nil
	}
	m.PxPerYard = ppy
	m.Width = min(wmoMaxSide, int(math.Ceil((m.West-m.East)*ppy)))
	m.Height = min(wmoMaxSide, int(math.Ceil((m.North-m.South)*ppy)))
	if m.Width <= 0 || m.Height <= 0 {
		return nil, false, "the WMO's blocks are too narrow for a picture", nil
	}
	var work int64
	for _, b := range list {
		r := b.rect(m)
		work += int64(r.Dx()) * int64(r.Dy())
		if work > wmoMaxWork {
			return nil, false, fmt.Sprintf("the WMO's blocks would sample more than %d pixels", wmoMaxWork), nil
		}
	}
	img := image.NewNRGBA(image.Rect(0, 0, m.Width, m.Height))
	for _, b := range list {
		b.draw(img, m)
	}
	m.Tiles = len(list)
	return &Layer{Kind: KindMinimap, Floor: suffix, Meta: m, Img: img}, true, "", nil
}

// WMOProbeOptions is one --wmo-probe run: the map ids to report, the server's dbc folder and where the report goes.
// The probe writes nothing else; every client byte stays where it is.
type WMOProbeOptions struct {
	Options
	Out io.Writer
}

// RunWMOProbe opens the CASC install and reports, per map, the WDT's WMO FileDataID, its groups' boxes and the
// WMOMinimapTexture blocks with the block size the geometry implies. It writes no files.
func RunWMOProbe(o WMOProbeOptions) error {
	if o.CascDir == "" {
		return errors.New("--wmo-probe needs --casc: it reads the install's WDT and WMO files")
	}
	if o.DB2Dir == "" {
		return errors.New("--wmo-probe needs --db2: Map.db2 names the WDT and WMOMinimapTexture.db2 the blocks")
	}
	s, err := casc.Open(o.CascDir, o.Product)
	if err != nil {
		return err
	}
	defer s.Close()
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	dbPath, ok := findFold(o.DB2Dir, "Map.db2")
	if !ok {
		return fmt.Errorf("Map.db2 is not in %s", o.DB2Dir)
	}
	dbf, err := db2.Open(dbPath)
	if err != nil {
		return err
	}
	rows, _, err := dbf.Maps()
	if err != nil {
		return err
	}
	byID := make(map[uint32]db2.Map, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	var blocks []db2.WMOMinimapTexture
	if p, ok := findFold(o.DB2Dir, "WMOMinimapTexture.db2"); ok {
		f, err := db2.Open(p)
		if err != nil {
			return err
		}
		if blocks, _, err = f.WMOMinimapTextures(); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(o.Out, "WMOMinimapTexture.db2 is not in %s; no block rows to report\n", o.DB2Dir)
	}
	for _, id := range o.Maps {
		r, ok := byID[id]
		if !ok {
			return fmt.Errorf("map %d is not in Map.db2", id)
		}
		if err := probeMap(s, o.Out, r, blocks, logf); err != nil {
			return err
		}
	}
	return nil
}

// probeMap writes one map's WMO report: the WDT's chunks, the root WMO, its group boxes, the block rows and the block
// size the geometry implies.
func probeMap(src fileSource, out io.Writer, r db2.Map, blocks []db2.WMOMinimapTexture, logf func(string, ...any)) error {
	fmt.Fprintf(out, "map %d %s [%s]: WDT FileDataID %d\n", r.ID, r.MapName, r.Directory, r.WdtFileDataID)
	if r.WdtFileDataID <= 0 {
		fmt.Fprintln(out, "  no WDT: not a WMO-only map")
		return nil
	}
	wdt, err := src.ReadFileDataID(uint32(r.WdtFileDataID))
	if err != nil {
		return fmt.Errorf("map %d: WDT FileDataID %d: %w", r.ID, r.WdtFileDataID, err)
	}
	w := parseWDTWMO(wdt)
	fmt.Fprintf(out, "  MPHD flags %#x; MWMO names %d, MWID ids %d, MODF placements %d\n",
		w.flags, len(w.names), len(w.ids), len(w.modfs))
	for i, m := range w.modfs {
		name := ""
		if int(m.nameID) < len(w.names) {
			name = " " + w.names[m.nameID]
		}
		fmt.Fprintf(out, "  MODF %d: Id %d%s uid %d flags %#x pos %.1f,%.1f,%.1f rot %.1f,%.1f,%.1f\n",
			i, m.nameID, name, m.uid, m.flags, m.pos[0], m.pos[1], m.pos[2], m.rot[0], m.rot[1], m.rot[2])
	}
	root, ok := w.rootID()
	if !ok {
		fmt.Fprintln(out, "  root WMO FileDataID: none (no MODF row names one)")
		return nil
	}
	fmt.Fprintf(out, "  root WMO FileDataID %d\n", root)
	rb, err := src.ReadFileDataID(root)
	if err != nil {
		return fmt.Errorf("map %d: root WMO FileDataID %d: %w", r.ID, root, err)
	}
	wmoID, hasMOHD := parseMOHD(rb)
	if !hasMOHD {
		fmt.Fprintln(out, "  MOHD: absent (no WMO id to key the block table on)")
		return nil
	}
	fmt.Fprintf(out, "  MOHD id %d\n", wmoID)
	groups := parseMOGI(rb)
	fmt.Fprintf(out, "  MOGI groups %d\n", len(groups))
	for i, g := range groups {
		if g == nil {
			fmt.Fprintf(out, "    group %d: implausible box; skipped\n", i)
			continue
		}
		fmt.Fprintf(out, "    group %d: x %.1f..%.1f y %.1f..%.1f z %.1f..%.1f yd\n",
			i, g.x0, g.x1, g.y0, g.y1, g.z0, g.z1)
	}
	rows := 0
	perGroup := map[int][2]int{} // group -> highest BlockX, BlockY
	for _, b := range blocks {
		if b.WMOID != wmoID {
			continue
		}
		rows++
		line := fmt.Sprintf("    block group %d at %d,%d FileDataID %d", b.GroupNum, b.BlockX, b.BlockY, b.FileDataID)
		// The texture's size against the block's box-clipped rectangle and its full 128-yard grid square: the
		// texture tells the density and what the block's art covers.
		if data, err := src.ReadFileDataID(uint32(b.FileDataID)); err == nil {
			if img, derr := decodeWMOTile(data); derr == nil {
				bb := img.Bounds()
				line += fmt.Sprintf(": %dx%d px", bb.Dx(), bb.Dy())
				if int(b.GroupNum) < len(groups) && groups[b.GroupNum] != nil {
					g := groups[b.GroupNum]
					cw := math.Min(g.x1, g.x0+(float64(b.BlockX)+1)*WMOBlockYards) - (g.x0 + float64(b.BlockX)*WMOBlockYards)
					ch := math.Min(g.y1, g.y0+(float64(b.BlockY)+1)*WMOBlockYards) - (g.y0 + float64(b.BlockY)*WMOBlockYards)
					if cw > 0 && ch > 0 {
						line += fmt.Sprintf(" (%.1f x %.1f clipped, %.2f x %.2f px/yd)", cw, ch, float64(bb.Dx())/cw, float64(bb.Dy())/ch)
					}
				}
			}
		}
		fmt.Fprintln(out, line)
		h := perGroup[int(b.GroupNum)]
		perGroup[int(b.GroupNum)] = [2]int{max(h[0], int(b.BlockX)), max(h[1], int(b.BlockY))}
	}
	if rows == 0 {
		logf("map %d: WMOMinimapTexture has no rows for WMO FileDataID %d", r.ID, root)
	}
	fmt.Fprintf(out, "  WMOMinimapTexture rows for WMO %d: %d\n", wmoID, rows)
	for gi, h := range perGroup {
		if gi >= len(groups) || groups[gi] == nil {
			continue
		}
		g := groups[gi]
		extX, extY := g.x1-g.x0, g.y1-g.y0
		nx, ny := h[0]+1, h[1]+1
		fmt.Fprintf(out, "    group %d: %d x %d blocks over %.1f x %.1f yd: %.1f..%.1f yd a block\n",
			gi, nx, ny, extX, extY, extX/float64(nx), blockUpper(extX, nx))
	}
	if rows > 0 {
		lo, hi, _ := wmoBlockRange(groups, perGroup)
		top := strconv.FormatFloat(hi, 'f', 2, 64)
		if math.IsInf(hi, 1) {
			top = "no upper bound (every group has one block)"
		}
		fmt.Fprintf(out, "  block size implied: more than %.2f and at most %s yd each way; WMOBlockYards = %s\n",
			lo, top, strconv.FormatFloat(WMOBlockYards, 'g', -1, 64))
	}
	return nil
}

// wmoBlockRange is the block size the groups' boxes and block counts imply, intersected over every group with blocks:
// a group of E yards covered by n blocks allows E/n < block <= E/(n-1). ok is false when no group names a box.
func wmoBlockRange(groups []*wmoGroup, perGroup map[int][2]int) (lo, hi float64, ok bool) {
	lo, hi = 0, math.Inf(1)
	for gi, h := range perGroup {
		if gi >= len(groups) || groups[gi] == nil {
			continue
		}
		g := groups[gi]
		ok = true
		lo = math.Max(lo, math.Max((g.x1-g.x0)/float64(h[0]+1), (g.y1-g.y0)/float64(h[1]+1)))
		hi = math.Min(hi, math.Min(blockUpper(g.x1-g.x0, h[0]+1), blockUpper(g.y1-g.y0, h[1]+1)))
	}
	return lo, hi, ok
}

// blockUpper is the largest block size n blocks over extent yards allow: n blocks cover the extent, so one block is
// at most extent/(n-1), and any size at all when the group has one block.
func blockUpper(extent float64, n int) float64 {
	if n <= 1 {
		return math.Inf(1)
	}
	return extent / float64(n-1)
}
