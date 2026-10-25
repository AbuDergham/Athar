// Command winres turns an .ico file into the Windows resource objects that
// give athar.exe its icon. go build links any rsrc_windows_<arch>.syso found
// in the package folder, so the icon needs no build step or dependency.
//
//	go run ./tools/winres -ico cmd/athar/winres/athar.ico -out cmd/athar
//
// It writes rsrc_windows_amd64.syso and rsrc_windows_arm64.syso: COFF objects
// with one .rsrc section holding RT_ICON images and the RT_GROUP_ICON that
// lists them (the first icon group is the one Explorer shows).
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	langEnUS    = 0x0409
	groupID     = 1
)

// arches maps GOARCH to the COFF machine and its 32-bit image-relative
// relocation type (IMAGE_REL_AMD64_ADDR32NB, IMAGE_REL_ARM64_ADDR32NB).
var arches = []struct {
	goarch  string
	machine uint16
	reloc   uint16
}{
	{"amd64", 0x8664, 0x0003},
	{"arm64", 0xAA64, 0x0002},
}

func main() {
	ico := flag.String("ico", "", "the .ico file")
	out := flag.String("out", ".", "folder for the .syso files")
	flag.Parse()
	if *ico == "" {
		fmt.Fprintln(os.Stderr, "usage: winres -ico <file.ico> [-out <folder>]")
		os.Exit(2)
	}
	if err := run(*ico, *out); err != nil {
		fmt.Fprintln(os.Stderr, "winres:", err)
		os.Exit(1)
	}
}

func run(icoPath, outDir string) error {
	data, err := os.ReadFile(icoPath)
	if err != nil {
		return err
	}
	images, group, err := parseICO(data)
	if err != nil {
		return fmt.Errorf("%s: %w", icoPath, err)
	}
	section, relocs := buildSection(images, group)
	for _, a := range arches {
		obj := coff(a.machine, a.reloc, section, relocs)
		name := filepath.Join(outDir, "rsrc_windows_"+a.goarch+".syso")
		if err := os.WriteFile(name, obj, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// parseICO returns the images of an .ico file and the RT_GROUP_ICON data
// that names them by resource ID 1..n.
func parseICO(b []byte) (images [][]byte, group []byte, err error) {
	if len(b) < 6 || binary.LittleEndian.Uint16(b[0:]) != 0 || binary.LittleEndian.Uint16(b[2:]) != 1 {
		return nil, nil, errors.New("not an icon file")
	}
	n := int(binary.LittleEndian.Uint16(b[4:]))
	if n == 0 || len(b) < 6+16*n {
		return nil, nil, errors.New("truncated icon directory")
	}
	var g bytes.Buffer
	binary.Write(&g, binary.LittleEndian, [3]uint16{0, 1, uint16(n)})
	for i := 0; i < n; i++ {
		e := b[6+16*i : 6+16*(i+1)]
		size := binary.LittleEndian.Uint32(e[8:])
		off := binary.LittleEndian.Uint32(e[12:])
		if uint64(off)+uint64(size) > uint64(len(b)) {
			return nil, nil, fmt.Errorf("image %d is outside the file", i)
		}
		images = append(images, b[off:off+size])
		// GRPICONDIRENTRY: the ICONDIRENTRY's first 12 bytes, then the resource ID.
		g.Write(e[:12])
		binary.Write(&g, binary.LittleEndian, uint16(i+1))
	}
	return images, g.Bytes(), nil
}

// buildSection lays out the resource directory tree (type, ID, language)
// followed by the resource data. relocs are the section offsets of the data
// entries' OffsetToData fields, which hold RVAs the linker must fix up.
func buildSection(images [][]byte, group []byte) (section []byte, relocs []uint32) {
	type leaf struct {
		id   uint16
		data []byte
	}
	types := []struct {
		id     uint16
		leaves []leaf
	}{{id: rtIcon}, {id: rtGroupIcon, leaves: []leaf{{groupID, group}}}}
	for i, img := range images {
		types[0].leaves = append(types[0].leaves, leaf{uint16(i + 1), img})
	}

	const dirSize, entrySize, dataEntrySize = 16, 8, 16
	// Offsets: root dir, one dir per type, one language dir per leaf, data entries, data.
	nLeaves := 0
	for _, t := range types {
		nLeaves += len(t.leaves)
	}
	rootSize := dirSize + entrySize*len(types)
	typeDirs := make([]uint32, len(types))
	off := uint32(rootSize)
	for i, t := range types {
		typeDirs[i] = off
		off += uint32(dirSize + entrySize*len(t.leaves))
	}
	langDirs := off
	off += uint32(nLeaves * (dirSize + entrySize))
	dataEntries := off
	off += uint32(nLeaves * dataEntrySize)

	var buf bytes.Buffer
	le := func(v any) { binary.Write(&buf, binary.LittleEndian, v) }
	// IMAGE_RESOURCE_DIRECTORY: characteristics, timestamp, version, named and ID entry counts
	dir := func(ids int) { le([2]uint32{}); le([4]uint16{0, 0, 0, uint16(ids)}) }
	const subdir = 0x80000000

	dir(len(types))
	for i, t := range types {
		le([2]uint32{uint32(t.id), subdir | typeDirs[i]})
	}
	leafIndex := 0
	for _, t := range types {
		dir(len(t.leaves))
		for j, l := range t.leaves {
			le([2]uint32{uint32(l.id), subdir | (langDirs + uint32((leafIndex+j)*(dirSize+entrySize)))})
		}
		leafIndex += len(t.leaves)
	}
	for i := 0; i < nLeaves; i++ {
		dir(1)
		le([2]uint32{langEnUS, dataEntries + uint32(i*dataEntrySize)})
	}
	// data entries, then the 8-byte aligned data they point at
	dataOff := align8(off)
	var data bytes.Buffer
	for _, t := range types {
		for _, l := range t.leaves {
			relocs = append(relocs, uint32(buf.Len()))
			le([4]uint32{dataOff + uint32(data.Len()), uint32(len(l.data)), 0, 0})
			data.Write(l.data)
			data.Write(make([]byte, align8(uint32(data.Len()))-uint32(data.Len())))
		}
	}
	if uint32(buf.Len()) != off {
		panic(fmt.Sprintf("winres: directory is %d bytes, laid out as %d", buf.Len(), off))
	}
	buf.Write(make([]byte, dataOff-off))
	buf.Write(data.Bytes())
	return buf.Bytes(), relocs
}

// coff wraps a .rsrc section in a COFF object: file header, section header,
// raw data, relocations against the section symbol, symbol table.
func coff(machine, relocType uint16, section []byte, relocs []uint32) []byte {
	const fileHeader, sectionHeader, relocSize = 20, 40, 10
	rawOff := uint32(fileHeader + sectionHeader)
	relocOff := rawOff + uint32(len(section))
	symOff := relocOff + uint32(relocSize*len(relocs))

	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	// IMAGE_FILE_HEADER
	le(machine)
	le(uint16(1)) // sections
	le(uint32(0)) // timestamp: reproducible output
	le(symOff)    // symbol table
	le(uint32(1)) // symbols
	le(uint16(0)) // optional header size
	le(uint16(0)) // characteristics
	// IMAGE_SECTION_HEADER
	b.WriteString(".rsrc\x00\x00\x00")
	le([2]uint32{0, 0}) // virtual size and address
	le(uint32(len(section)))
	le(rawOff)
	le(relocOff)
	le(uint32(0)) // line numbers
	le(uint16(len(relocs)))
	le(uint16(0))
	le(uint32(0x40000040)) // initialized data, readable
	b.Write(section)
	for _, r := range relocs {
		le(r)
		le(uint32(0)) // symbol 0: the section
		le(relocType)
	}
	// IMAGE_SYMBOL for the section, then an empty string table
	b.WriteString(".rsrc\x00\x00\x00")
	le(uint32(0))  // value
	le(int16(1))   // section number
	le(uint16(0))  // type
	b.WriteByte(3) // IMAGE_SYM_CLASS_STATIC
	b.WriteByte(0) // aux symbols
	le(uint32(4))
	return b.Bytes()
}

func align8(n uint32) uint32 { return (n + 7) &^ 7 }
