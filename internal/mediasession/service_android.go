//go:build android

package mediasession

// This file is the JNI plumbing and nothing else: the state logic it drives
// lives in mediasession.go, where the desktop can test it. It talks to the
// static half of PlaybackService.java; commands coming BACK from Java arrive
// in export_android.go (cgo forbids C definitions in a file with //export,
// which is why the two are separate files).

/*
#include <jni.h>
#include <stdlib.h>

// JNIEnv is a pointer to a function table, and Go cannot call through C
// function pointers — hence one C shim per JNI verb, the same shape Gio's own
// Android layer uses. The signatures are the NDK's: on Android
// AttachCurrentThreadAsDaemon takes JNIEnv**, not the JDK's void**.

static jint ms_attach(JavaVM *vm, JNIEnv **env) {
	return (*vm)->AttachCurrentThreadAsDaemon(vm, env, NULL);
}
static jclass ms_object_class(JNIEnv *env, jobject obj) {
	return (*env)->GetObjectClass(env, obj);
}
static jmethodID ms_method(JNIEnv *env, jclass cls, const char *name, const char *sig) {
	return (*env)->GetMethodID(env, cls, name, sig);
}
static jmethodID ms_static_method(JNIEnv *env, jclass cls, const char *name, const char *sig) {
	return (*env)->GetStaticMethodID(env, cls, name, sig);
}
static jobject ms_call_object(JNIEnv *env, jobject obj, jmethodID m, jvalue *args) {
	return (*env)->CallObjectMethodA(env, obj, m, args);
}
static void ms_call_static_void(JNIEnv *env, jclass cls, jmethodID m, jvalue *args) {
	(*env)->CallStaticVoidMethodA(env, cls, m, args);
}
static jstring ms_new_string(JNIEnv *env, const char *modutf8) {
	return (*env)->NewStringUTF(env, modutf8);
}
static jobject ms_new_global_ref(JNIEnv *env, jobject obj) {
	return (*env)->NewGlobalRef(env, obj);
}
static void ms_delete_local_ref(JNIEnv *env, jobject obj) {
	(*env)->DeleteLocalRef(env, obj);
}
// ms_check_exception clears a pending exception and reports it, dumping the
// stack trace to logcat on the way — swallowing it silently would turn every
// Java-side mistake into "the notification just doesn't update".
static jboolean ms_check_exception(JNIEnv *env) {
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
	"errors"
	"fmt"
	"log"
	"runtime"
	"sync/atomic"
	"time"
	"unsafe"

	"gioui.org/app"
)

// The command vocabulary shared with PlaybackService.java. The numbers ARE the
// protocol; export_android.go dispatches on them.
const (
	cmdPlay   = 0
	cmdPause  = 1
	cmdNext   = 2
	cmdPrev   = 3
	cmdStop   = 4
	cmdSeekMS = 5
)

// serviceClass is looked up through the app's class loader; the Java package
// is welded to the JNI export name in export_android.go and to the manifest
// entry android/build-apk.sh writes.
const serviceClass = "ygg.daemonlord.madplayer.PlaybackService"

// shutdownLinger is how long an inactive snapshot must persist before the
// service is stopped — long enough to outlast playCurrent's open-a-local-
// track blip by orders of magnitude, short enough that a genuine stop takes
// the notification down before anybody wonders about it.
const shutdownLinger = 2 * time.Second

// commands is what nativeCommand dispatches to. Package-level because a JNI
// static native carries no Go receiver.
var commands atomic.Pointer[controlsBox]

type controlsBox struct{ c Controls }

// Service drives the Java side from one dedicated OS thread. JNI hands out an
// env per attached thread, so all calls funnel through the loop goroutine; the
// player's hooks only deposit a snapshot and knock.
type Service struct {
	c      Controls
	kick   chan struct{}
	done   chan struct{}
	latest atomic.Pointer[snapshot]
}

// New starts the bridge. It cannot fail loudly by design: a broken Java half
// (the jar missing from the APK, an old class) costs the lock-screen controls
// and logs once, never the program — the MPRIS rule again.
func New(c Controls) *Service {
	s := &Service{
		c:    c,
		kick: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
	commands.Store(&controlsBox{c})
	go s.loop()
	return s
}

// Update recomputes what Android can see and wakes the pusher. It is called
// from the player's change hook and must stay cheap; the JNI work happens on
// the loop's thread.
func (s *Service) Update() {
	if s == nil {
		return
	}
	snap := observe(s.c)
	s.latest.Store(&snap)
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Tick keeps the lock screen's progress bar honest after in-app seeks; clients
// interpolate position from the last stamp, so it only matters while moving.
func (s *Service) Tick() {
	if s == nil || !s.c.Playing() {
		return
	}
	s.Update()
}

// Close stops the pusher and takes the notification down with it.
func (s *Service) Close() {
	if s == nil {
		return
	}
	close(s.done)
}

func (s *Service) loop() {
	// One OS thread for the life of the bridge: the attached JNIEnv is
	// thread-local, and attaching as daemon means the JVM never waits for us.
	runtime.LockOSThread()
	j, err := bind()
	if err != nil {
		log.Printf("madplayer: lock-screen and background controls unavailable: %v", err)
		return
	}

	started := false
	var last snapshot
	// quiet delays acting on an inactive snapshot. A LOCAL track being
	// opened has no ctrl and no loading flag for a few milliseconds
	// (player.playCurrent nils everything before its load goroutine runs,
	// and only a download sets loading), so every advance onto a local
	// track publishes one inactive snapshot. Acting on it flapped the
	// service — shutdown, then start on the very next snapshot — and the
	// restart is FATAL with the app in the background: Android refuses
	// startForegroundService there and the refusal is an exception (three
	// crashes in one day's log, each at a screen-off track boundary,
	// 2026-08-18). A real stop stays inactive and outlives the linger.
	var quiet <-chan time.Time
	for {
		select {
		case <-s.done:
			if started {
				j.shutdown()
			}
			return
		case <-quiet:
			quiet = nil
			if snap := s.latest.Load(); snap != nil && snap.active {
				continue // it came back before the linger ran out
			}
			if started {
				j.shutdown()
				started, last = false, snapshot{}
			}
			continue
		case <-s.kick:
		}
		snap := s.latest.Load()
		if snap == nil {
			continue
		}
		if !snap.active {
			if started && quiet == nil {
				quiet = time.After(shutdownLinger)
			}
			continue
		}
		quiet = nil
		if !started {
			j.start()
			started = true
		}
		if !snap.sameTrack(last) {
			j.setMetadata(snap.title, snap.artist, snap.album, snap.durationMs, snap.artPath)
		}
		j.setState(snap.playing, snap.positionMs)
		last = *snap
	}
}

// java is the bound far side: an attached env, the service class and its four
// static entry points. Valid only on the loop's locked thread.
type java struct {
	env          *C.JNIEnv
	ctx          C.jobject
	cls          C.jclass
	mStart       C.jmethodID
	mSetMetadata C.jmethodID
	mSetState    C.jmethodID
	mShutdown    C.jmethodID
}

// bind attaches to the JVM and resolves the Java half. FindClass is useless
// from a native thread (its loader only knows the system classes), so the
// class comes through the application context's own loader — the standard
// dance for app classes reached from JNI.
func bind() (*java, error) {
	vm := (*C.JavaVM)(unsafe.Pointer(app.JavaVM()))
	ctx := C.jobject(unsafe.Pointer(app.AppContext()))
	if vm == nil || ctx == 0 {
		return nil, errors.New("no JVM in this process")
	}
	j := &java{ctx: ctx}
	if C.ms_attach(vm, &j.env) != 0 {
		return nil, errors.New("AttachCurrentThreadAsDaemon failed")
	}

	ctxCls := C.ms_object_class(j.env, ctx)
	getLoader := j.method(ctxCls, "getClassLoader", "()Ljava/lang/ClassLoader;")
	loader := C.ms_call_object(j.env, ctx, getLoader, nil)
	if C.ms_check_exception(j.env) != 0 || loader == 0 {
		return nil, errors.New("getClassLoader failed")
	}
	loadClass := j.method(C.ms_object_class(j.env, loader), "loadClass", "(Ljava/lang/String;)Ljava/lang/Class;")

	name := j.str(serviceClass)
	args := []C.jvalue{jvalObj(C.jobject(name))}
	cls := C.ms_call_object(j.env, loader, loadClass, &args[0])
	C.ms_delete_local_ref(j.env, C.jobject(name))
	if C.ms_check_exception(j.env) != 0 || cls == 0 {
		return nil, fmt.Errorf("%s is not in this APK — was the jar built and dexed in? (android/build-apk.sh does both)", serviceClass)
	}
	j.cls = C.jclass(C.ms_new_global_ref(j.env, cls))
	C.ms_delete_local_ref(j.env, cls)

	for _, m := range []struct {
		id   *C.jmethodID
		name string
		sig  string
	}{
		{&j.mStart, "start", "(Landroid/content/Context;)V"},
		{&j.mSetMetadata, "setMetadata", "(Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;JLjava/lang/String;)V"},
		{&j.mSetState, "setState", "(ZJ)V"},
		{&j.mShutdown, "shutdown", "()V"},
	} {
		*m.id = j.staticMethod(j.cls, m.name, m.sig)
		if C.ms_check_exception(j.env) != 0 || *m.id == nil {
			return nil, fmt.Errorf("%s.%s%s missing — Java and Go halves out of step", serviceClass, m.name, m.sig)
		}
	}
	return j, nil
}

func (j *java) start() {
	args := []C.jvalue{jvalObj(j.ctx)}
	j.callStatic(j.mStart, &args[0])
}

func (j *java) setMetadata(title, artist, album string, durationMs int64, artPath string) {
	strs := []C.jstring{j.str(title), j.str(artist), j.str(album), j.str(artPath)}
	args := []C.jvalue{
		jvalObj(C.jobject(strs[0])),
		jvalObj(C.jobject(strs[1])),
		jvalObj(C.jobject(strs[2])),
		jvalLong(durationMs),
		jvalObj(C.jobject(strs[3])),
	}
	j.callStatic(j.mSetMetadata, &args[0])
	// The thread never returns to Java, so local refs are never collected for
	// us; leaking one per track would add up over an evening of albums.
	for _, s := range strs {
		C.ms_delete_local_ref(j.env, C.jobject(s))
	}
}

func (j *java) setState(playing bool, positionMs int64) {
	args := []C.jvalue{jvalBool(playing), jvalLong(positionMs)}
	j.callStatic(j.mSetState, &args[0])
}

func (j *java) shutdown() {
	j.callStatic(j.mShutdown, nil)
}

func (j *java) callStatic(m C.jmethodID, args *C.jvalue) {
	C.ms_call_static_void(j.env, j.cls, m, args)
	C.ms_check_exception(j.env)
}

func (j *java) method(cls C.jclass, name, sig string) C.jmethodID {
	cn, cs := C.CString(name), C.CString(sig)
	defer C.free(unsafe.Pointer(cn))
	defer C.free(unsafe.Pointer(cs))
	return C.ms_method(j.env, cls, cn, cs)
}

func (j *java) staticMethod(cls C.jclass, name, sig string) C.jmethodID {
	cn, cs := C.CString(name), C.CString(sig)
	defer C.free(unsafe.Pointer(cn))
	defer C.free(unsafe.Pointer(cs))
	return C.ms_static_method(j.env, cls, cn, cs)
}

// str builds a Java string. Through modUTF8, because NewStringUTF wants
// MODIFIED UTF-8 and aborts the process under CheckJNI when an emoji in a
// track title arrives as the real thing (modutf8.go has the story).
func (j *java) str(s string) C.jstring {
	b := modUTF8(s)
	return C.ms_new_string(j.env, (*C.char)(unsafe.Pointer(&b[0])))
}

// The jvalue union arrives in Go as an opaque byte array; each helper writes
// the one member the call wants. Android is little-endian on every ABI this
// builds for, so the member sits at offset 0.

func jvalObj(o C.jobject) C.jvalue {
	var v C.jvalue
	*(*C.jobject)(unsafe.Pointer(&v)) = o
	return v
}

func jvalLong(x int64) C.jvalue {
	var v C.jvalue
	*(*C.jlong)(unsafe.Pointer(&v)) = C.jlong(x)
	return v
}

func jvalBool(b bool) C.jvalue {
	var v C.jvalue
	if b {
		*(*C.jboolean)(unsafe.Pointer(&v)) = 1
	}
	return v
}
