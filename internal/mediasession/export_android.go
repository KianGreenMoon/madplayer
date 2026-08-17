//go:build android

package mediasession

// The road back: PlaybackService.java's `static native void nativeCommand`
// resolves to the export below by JNI's naming convention — no RegisterNatives,
// the symbol name in libgio.so IS the wiring. It is a separate file because a
// cgo file containing //export may only declare C things, never define them,
// and service_android.go's shims are definitions.

/*
#include <jni.h>
*/
import "C"

//export Java_ygg_daemonlord_madplayer_PlaybackService_nativeCommand
func Java_ygg_daemonlord_madplayer_PlaybackService_nativeCommand(env *C.JNIEnv, cls C.jclass, cmd C.jint, arg C.jlong) {
	box := commands.Load()
	if box == nil || box.c == nil {
		return
	}
	c := box.c
	// Dispatched off the calling thread: it is Android's main looper or a
	// binder thread, and neither may block on a decoder swap. The player is
	// already called from several goroutines (window, MPRIS) and locks for
	// itself.
	go func() {
		switch int32(cmd) {
		case cmdPlay:
			c.Play()
		case cmdPause:
			c.Pause()
		case cmdNext:
			c.Next()
		case cmdPrev:
			c.Prev()
		case cmdStop:
			c.Stop()
		case cmdSeekMS:
			c.Seek(float64(int64(arg)) / 1000)
		}
	}()
}
