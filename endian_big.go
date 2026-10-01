//go:build armbe || arm64be || m68k || mips || mips64 || mips64p32 || ppc || ppc64 || s390 || s390x || shbe || sparc || sparc64

package stilus

// bigEndian reports the host byte order (the build constraints of
// encoding/binary.NativeEndian): pixels are native-endian uint32 views of
// RGBA bytes.
const bigEndian = true
