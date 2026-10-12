// Package db 持有 GORM 连接，并把事务放在 ctx 里传递。只有 repo 和 pkg/tx 可以 import 本包。
package db

import (
	"context"
	"sync"

	"gorm.io/gorm"
)

// DB 包装根连接。repo 一律经 Conn(ctx) 取连接：ctx 里有事务就用事务，否则用根连接。
type DB struct {
	base *gorm.DB
}

// Wrap 包装一个已打开的 *gorm.DB。
func Wrap(base *gorm.DB) *DB { return &DB{base: base} }

// Conn 返回 ctx 对应的连接，已带 WithContext。
func (d *DB) Conn(ctx context.Context) *gorm.DB {
	if st := stateFrom(ctx); st != nil {
		return st.tx.WithContext(ctx)
	}
	return d.base.WithContext(ctx)
}

// Base 返回根连接，只给 pkg/tx 与启动装配用。
func (d *DB) Base() *gorm.DB { return d.base }

// TxState 是放在 ctx 里的事务状态，由 pkg/tx 创建。
type TxState struct {
	tx    *gorm.DB
	mu    sync.Mutex
	keys  map[string]struct{}
	hooks []func(context.Context)
}

type stateKey struct{}

// NewTxState 由 pkg/tx 在开启事务时调用。
func NewTxState(tx *gorm.DB) *TxState {
	return &TxState{tx: tx, keys: map[string]struct{}{}}
}

// WithTxState 把事务状态放进 ctx。
func WithTxState(ctx context.Context, st *TxState) context.Context {
	return context.WithValue(ctx, stateKey{}, st)
}

// InTx 报告 ctx 里是否有事务。
func InTx(ctx context.Context) bool { return stateFrom(ctx) != nil }

// AddHook 登记提交后执行的函数；key 非空时同一事务内只登记一次。返回 false 表示 ctx 里没有事务。
func AddHook(ctx context.Context, key string, fn func(context.Context)) bool {
	st := stateFrom(ctx)
	if st == nil {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if key != "" {
		if _, dup := st.keys[key]; dup {
			return true
		}
		st.keys[key] = struct{}{}
	}
	st.hooks = append(st.hooks, fn)
	return true
}

// Hooks 返回已登记的提交后函数（按登记顺序）。
func (st *TxState) Hooks() []func(context.Context) {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]func(context.Context), len(st.hooks))
	copy(out, st.hooks)
	return out
}

func stateFrom(ctx context.Context) *TxState {
	st, _ := ctx.Value(stateKey{}).(*TxState)
	return st
}
