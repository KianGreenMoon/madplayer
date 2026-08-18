//go:build android

package audio

// The one JNI call this package makes: asking Android what rate its output
// path actually runs at. AudioTrack.getNativeOutputSampleRate is a static on
// a system class, so plain FindClass resolves it from a native thread — none
// of mediasession's class-loader dance is needed here.

/*
#include <jni.h>
#include <stdlib.h>

// JNIEnv is a table of function pointers Go cannot call through — one C shim
// per verb, the same shape as internal/mediasession/service_android.go.

static jint au_attach(JavaVM *vm, JNIEnv **env) {
	return (*vm)->AttachCurrentThreadAsDaemon(vm, env, NULL);
}
static jclass au_find_class(JNIEnv *env, const char *name) {
	return (*env)->FindClass(env, name);
}
static jmethodID au_static_method(JNIEnv *env, jclass cls, const char *name, const char *sig) {
	return (*env)->GetStaticMethodID(env, cls, name, sig);
}
static jint au_call_static_int(JNIEnv *env, jclass cls, jmethodID m, jint arg) {
	jvalue v;
	v.i = arg;
	return (*env)->CallStaticIntMethodA(env, cls, m, &v);
}
static jboolean au_clear_exception(JNIEnv *env) {
	if (!(*env)->ExceptionCheck(env)) {
		return 0;
	}
	(*env)->ExceptionDescribe(env);
	(*env)->ExceptionClear(env);
	return 1;
}
*/
import "C"

import (
	"runtime"
	"unsafe"

	"gioui.org/app"
)

// streamMusic is android.media.AudioManager.STREAM_MUSIC.
const streamMusic = 3

// nativeOutputSampleRate asks the device for its output path's own rate —
// what AudioTrack.getNativeOutputSampleRate(STREAM_MUSIC) answers, 48000 on
// most phones. 0 means the answer could not be had and the caller should
// fall back to its requested rate.
func nativeOutputSampleRate() int {
	// The attached JNIEnv is thread-local; pin the thread for the duration.
	// Attached as daemon and never detached, matching mediasession — the VM
	// outlives every thread of ours either way.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	vm := (*C.JavaVM)(unsafe.Pointer(app.JavaVM()))
	if vm == nil {
		return 0
	}
	var env *C.JNIEnv
	if C.au_attach(vm, &env) != 0 {
		return 0
	}
	cn, mn, sig := C.CString("android/media/AudioTrack"), C.CString("getNativeOutputSampleRate"), C.CString("(I)I")
	defer C.free(unsafe.Pointer(cn))
	defer C.free(unsafe.Pointer(mn))
	defer C.free(unsafe.Pointer(sig))
	cls := C.au_find_class(env, cn)
	if C.au_clear_exception(env) != 0 || cls == 0 {
		return 0
	}
	m := C.au_static_method(env, cls, mn, sig)
	if C.au_clear_exception(env) != 0 || m == nil {
		return 0
	}
	rate := int(C.au_call_static_int(env, cls, m, streamMusic))
	if C.au_clear_exception(env) != 0 || rate <= 0 {
		return 0
	}
	return rate
}
