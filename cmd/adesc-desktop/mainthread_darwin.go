//go:build darwin

package main

/*
#include <dispatch/dispatch.h>
#include <stdint.h>

extern void adescRunOnMain(uintptr_t h);

static void adesc_trampoline(void *ctx) { adescRunOnMain((uintptr_t)ctx); }

static void adesc_dispatch_main(uintptr_t h) {
	dispatch_async_f(dispatch_get_main_queue(), (void *)h, adesc_trampoline);
}
*/
import "C"

import "runtime/cgo"

//export adescRunOnMain
func adescRunOnMain(h C.uintptr_t) {
	handle := cgo.Handle(h)
	f := handle.Value().(func())
	handle.Delete()
	f()
}

// runOnMainThread runs f asynchronously on the main (AppKit) thread.
func runOnMainThread(f func()) {
	C.adesc_dispatch_main(C.uintptr_t(cgo.NewHandle(f)))
}
