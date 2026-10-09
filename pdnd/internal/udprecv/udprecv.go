// Package udprecv 是 UDP 转发下行的收包原语：阻塞到 socket 可读之后才借缓冲收一批，
// 空闲等待时不占任何收包缓冲，也不多做一次窥视。
//
// 做法：在 syscall.RawConn.Read 的回调里非阻塞地收（Linux recvmmsg，一次一批；别的
// unix 一次一包）。回调第一次被调时若 socket 是空的（EAGAIN），把缓冲还回去、返回
// false，由 Go 的 netpoller 挂起等可读，可读后再调一次回调。一次醒来的系统调用是
// 「落空 + 收到」两次，和阻塞读（sing-box 等）一样；之前「先窥视、再收」是三次。
//
// 来源地址直接从 sockaddr 解析成 netip.AddrPort，不像 x/net 那样每包分配 net.UDPAddr。
package udprecv

import (
	"errors"
	"net/netip"
	"syscall"
)

// ErrWouldBlock 是非阻塞收包时 socket 已空。
var ErrWouldBlock = errors.New("udprecv: would block")

// Batch 是一组可复用的收包缓冲与收包用的内核结构。同一时刻只被一个 goroutine 用。
type Batch struct {
	// Bufs 是各条消息的缓冲，N 是收到的长度，From 是来源。
	Bufs [][]byte
	N    []int
	From []netip.AddrPort
	sys  batchSys
}

// NewBatch 用给定的缓冲建一组（缓冲的长度就是每条能收的最大包长）。
func NewBatch(bufs [][]byte) *Batch {
	b := &Batch{Bufs: bufs, N: make([]int, len(bufs)), From: make([]netip.AddrPort, len(bufs))}
	b.sys.init(bufs)
	return b
}

// Receiver 是一个 socket 上的收包器，只给一个 goroutine 用（UDP 转发的下行）。回调
// 在建的时候绑好一次，每次收包不分配。
type Receiver struct {
	rc syscall.RawConn
	// 一次调用里回调要用的参数与结果。
	b        *Batch
	size     int
	n        int
	err      error
	borrow   func() *Batch
	giveBack func(*Batch)
	ready    func(uintptr) bool
	wait     func(uintptr) bool
	try      func(uintptr)
}

// NewReceiver 在 rc 上建收包器；本平台不支持时返回 nil（调用方退回阻塞 ReadFrom）。
func NewReceiver(rc syscall.RawConn) *Receiver {
	if !Supported || rc == nil {
		return nil
	}
	r := &Receiver{rc: rc}
	r.ready = r.readyRead
	r.wait = r.waitRead
	r.try = r.tryRead
	return r
}

// Ready 阻塞到 socket 可读，再向 borrow 借一组非阻塞收至多 size 条；socket 是空的就把
// 那组还给 giveBack 接着等，所以等待期间不占缓冲。返回借到的组（调用方用完归还）与
// 条数。出错时组已归还；读截止到点返回的错误满足 errors.Is(err, os.ErrDeadlineExceeded)。
func (r *Receiver) Ready(size int, borrow func() *Batch, giveBack func(*Batch)) (*Batch, int, error) {
	r.b, r.size, r.n, r.err, r.borrow, r.giveBack = nil, size, 0, nil, borrow, giveBack
	err := r.rc.Read(r.ready)
	b, n := r.b, r.n
	if err == nil {
		err = r.err
	}
	r.b, r.err, r.borrow, r.giveBack = nil, nil, nil, nil
	if err != nil {
		if b != nil {
			giveBack(b)
		}
		return nil, 0, err
	}
	return b, n, nil
}

func (r *Receiver) readyRead(fd uintptr) bool {
	if r.b == nil {
		r.b = r.borrow()
	}
	r.n, r.err = r.b.read(fd, r.size)
	if r.err == ErrWouldBlock {
		r.giveBack(r.b)
		r.b, r.err = nil, nil
		return false
	}
	return true
}

// Recv 用 b 收至多 size 条：wait 时阻塞到有包（受读截止约束），否则非阻塞，socket
// 已空返回 ErrWouldBlock。
func (r *Receiver) Recv(b *Batch, size int, wait bool) (int, error) {
	r.b, r.size, r.n, r.err = b, size, 0, nil
	var err error
	if wait {
		err = r.rc.Read(r.wait)
	} else {
		err = r.rc.Control(r.try)
	}
	n := r.n
	if err == nil {
		err = r.err
	}
	r.b, r.err = nil, nil
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (r *Receiver) waitRead(fd uintptr) bool {
	r.n, r.err = r.b.read(fd, r.size)
	if r.err == ErrWouldBlock {
		r.err = nil
		return false
	}
	return true
}

func (r *Receiver) tryRead(fd uintptr) { r.n, r.err = r.b.read(fd, r.size) }

// addrPort 把内核 sockaddr 转成 netip.AddrPort（IPv4 映射地址还原成 IPv4）。
func addrPort(family uint16, port uint16, addr4 [4]byte, addr16 [16]byte, zone uint32) netip.AddrPort {
	switch family {
	case syscall.AF_INET:
		return netip.AddrPortFrom(netip.AddrFrom4(addr4), port)
	case syscall.AF_INET6:
		_ = zone
		return netip.AddrPortFrom(netip.AddrFrom16(addr16).Unmap(), port)
	}
	return netip.AddrPort{}
}
