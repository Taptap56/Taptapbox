// udpgw.go — client protokol BadVPN udpgw (dipakai relay BadVPN untuk SSH).
//
// Konteks: outbound SSH di sing-box/mihomo TIDAK mendukung UDP ("UDP is
// not supported by outbound"). BadVPN menyelesaikan ini TANPA lewat
// outbound SSH core: paket UDP dibungkus protokol udpgw, dikirim lewat
// satu koneksi TCP yang diteruskan core (SOCKS mixed) via terowongan SSH
// ke daemon badvpn-udpgw yang jalan di server (umumnya 127.0.0.1:7300
// di sisi server). Tanpa udpgw di server, jalur ini tidak bisa jalan —
// kegagalan dial dilaporkan jelas di log, bukan diam-diam.
//
// Format pesan (satu pesan per datagram UDP, di atas satu koneksi TCP):
//   [size 2 byte LE][flags 1 byte][connID 2 byte LE][addr][port 2 byte BE][payload]
//   size   = panjang SEMUA byte sesudah field size (bukan total pesan).
//   flags  = bit: 0x01 keepalive (tanpa addr/payload, diabaikan server),
//            0x02 rebind (buang koneksi lama dengan connID sama),
//            0x04 DNS (teruskan ke resolver server; TIDAK dipakai di sini),
//            0x08 IPv6 (addr 16 byte, bukan 4 byte).
//   connID = ID multiplexing per aliran UDP (satu socket UDP per connID
//            di sisi server). Paket data biasa: flags 0x00 (IPv4) / 0x08.
//   addr   = IP tujuan mentah (4 byte IPv4 / 16 byte IPv6, TANPA domain —
//            domain harus di-resolve dulu oleh pemanggil).
//   payload = datagram UDP mentah, maks 32768 byte.
// Pesan keepalive: size=3, flags=0x01, connID=0, tanpa addr/payload.
// Pesan balasan server memakai format yang sama; alamatnya adalah alamat
// peer UDP yang membalas (server game), bukan alamat klien.

package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	udpgwFlagKeepalive = 0x01
	udpgwFlagRebind    = 0x02
	udpgwFlagDNS       = 0x04
	udpgwFlagIPv6      = 0x08

	udpgwMaxPayload = 32768
	// 2 (size) + 3 (flags+connID) + 18 (addr IPv6+port) + payload.
	udpgwMaxMessage        = 2 + 3 + 18 + udpgwMaxPayload
	udpgwKeepaliveInterval = 30 * time.Second
)

// udpgwMessage = satu pesan udpgw yang sudah di-decode.
type udpgwMessage struct {
	Flags   uint8
	ConnID  uint16
	IP      net.IP
	Port    int
	Payload []byte
}

// encodeUdpgwMessage mengemas satu datagram UDP menjadi satu pesan udpgw.
// ip harus IP hasil resolve (bukan domain); ip4 dipakai bila To4() ada.
func encodeUdpgwMessage(flags uint8, connID uint16, ip net.IP, port int, payload []byte) ([]byte, error) {
	if len(payload) > udpgwMaxPayload {
		return nil, fmt.Errorf("udpgw: payload %d melebihi %d", len(payload), udpgwMaxPayload)
	}
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("udpgw: port %d di luar jangkauan", port)
	}
	var addr []byte
	if ip4 := ip.To4(); ip4 != nil {
		addr = []byte(ip4)
		flags &^= udpgwFlagIPv6
	} else if ip16 := ip.To16(); ip16 != nil {
		addr = []byte(ip16)
		flags |= udpgwFlagIPv6
	} else {
		return nil, fmt.Errorf("udpgw: IP tidak valid")
	}
	size := 3 + len(addr) + 2 + len(payload)
	out := make([]byte, 2+size)
	binary.LittleEndian.PutUint16(out[0:2], uint16(size))
	out[2] = flags
	binary.LittleEndian.PutUint16(out[3:5], connID)
	copy(out[5:], addr)
	binary.BigEndian.PutUint16(out[5+len(addr):], uint16(port))
	copy(out[5+len(addr)+2:], payload)
	return out, nil
}

// encodeUdpgwKeepalive = pesan keepalive (size=3, tanpa addr/payload).
func encodeUdpgwKeepalive() []byte {
	return []byte{0x03, 0x00, udpgwFlagKeepalive, 0x00, 0x00}
}

// decodeUdpgwMessage mengurai body pesan (TANPA field size 2 byte).
// Mengembalikan error bila panjang tidak konsisten dengan flag IPv6.
func decodeUdpgwMessage(body []byte) (udpgwMessage, error) {
	var m udpgwMessage
	if len(body) < 3 {
		return m, fmt.Errorf("udpgw: pesan terlalu pendek (%d)", len(body))
	}
	m.Flags = body[0]
	m.ConnID = binary.LittleEndian.Uint16(body[1:3])
	if m.Flags&udpgwFlagKeepalive != 0 {
		return m, nil // keepalive: tanpa addr/payload, sisanya diabaikan
	}
	addrLen := 4
	if m.Flags&udpgwFlagIPv6 != 0 {
		addrLen = 16
	}
	if len(body) < 3+addrLen+2 {
		return m, fmt.Errorf("udpgw: pesan terpotong (flags=%#x, len=%d)", m.Flags, len(body))
	}
	m.IP = net.IP(append([]byte(nil), body[3:3+addrLen]...))
	m.Port = int(binary.BigEndian.Uint16(body[3+addrLen:]))
	m.Payload = append([]byte(nil), body[3+addrLen+2:]...)
	return m, nil
}

// readUdpgwMessage membaca satu pesan penuh dari stream TCP.
func readUdpgwMessage(r io.Reader) (udpgwMessage, error) {
	var sizeBuf [2]byte
	if _, err := io.ReadFull(r, sizeBuf[:]); err != nil {
		return udpgwMessage{}, err
	}
	size := int(binary.LittleEndian.Uint16(sizeBuf[:]))
	if size < 3 || size > udpgwMaxMessage-2 {
		return udpgwMessage{}, fmt.Errorf("udpgw: size tidak valid (%d)", size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return udpgwMessage{}, err
	}
	return decodeUdpgwMessage(body)
}

// udpgwClient = satu koneksi TCP ke udpgw server + reader/keepalive.
// onMessage dipanggil dari goroutine reader; harus tidak memblokir lama.
type udpgwClient struct {
	conn      net.Conn
	writeMu   sync.Mutex
	onMessage func(m udpgwMessage)
	onError   func(err error)
	done      chan struct{}
	closeOnce sync.Once
}

func newUdpgwClient(conn net.Conn, onMessage func(m udpgwMessage), onError func(err error)) *udpgwClient {
	return &udpgwClient{conn: conn, onMessage: onMessage, onError: onError, done: make(chan struct{})}
}

// run menjalankan reader + keepalive sampai koneksi ditutup. Blokir;
// jalankan di goroutine sendiri.
func (c *udpgwClient) run() {
	ka := time.NewTicker(udpgwKeepaliveInterval)
	defer ka.Stop()
	errCh := make(chan error, 1)
	go func() {
		for {
			m, err := readUdpgwMessage(c.conn)
			if err != nil {
				errCh <- err
				return
			}
			if m.Flags&udpgwFlagKeepalive != 0 {
				continue
			}
			if c.onMessage != nil {
				c.onMessage(m)
			}
		}
	}()
	for {
		select {
		case <-c.done:
			return
		case err := <-errCh:
			if c.onError != nil {
				c.onError(err)
			}
			return
		case <-ka.C:
			if err := c.sendRaw(encodeUdpgwKeepalive()); err != nil {
				if c.onError != nil {
					c.onError(err)
				}
				return
			}
		}
	}
}

// send mengirim satu datagram (flags biasanya 0x00/0x08, atau |rebind).
func (c *udpgwClient) send(flags uint8, connID uint16, ip net.IP, port int, payload []byte) error {
	msg, err := encodeUdpgwMessage(flags, connID, ip, port, payload)
	if err != nil {
		return err
	}
	return c.sendRaw(msg)
}

func (c *udpgwClient) sendRaw(msg []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.conn.Write(msg)
	return err
}

func (c *udpgwClient) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}
