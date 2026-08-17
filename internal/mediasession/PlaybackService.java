// PlaybackService is madplayer's presence on Android: the foreground service
// that keeps the process alive with the screen off, and the MediaSession that
// puts the current track on the lock screen, in the quick-settings media
// carousel and under every Bluetooth play button.
//
// It is the MPRIS story again on the other platform (internal/mpris), and the
// same two properties shape it. It is OPTIONAL — the Go side logs one line and
// carries on if anything here is missing — and it is a VIEW: no playback state
// lives here beyond a mirror of what Go last pushed, and every control gesture
// goes straight back to Go via nativeCommand rather than being acted on
// locally. The mirror exists only because Android asks the service for a
// notification at moments of its own choosing.
//
// This file rides into the APK as a jar: gogio dexes any *.jar it finds in a
// Go package directory, and android/build-apk.sh compiles this into one. The
// matching <service> manifest entry is inserted by the patched gogio the same
// script builds — stock gogio's manifest template cannot declare a service.
//
// The Java package is fixed by two contracts at once: the manifest entry the
// build script writes, and the JNI name of nativeCommand's Go export
// (Java_ygg_daemonlord_madplayer_PlaybackService_nativeCommand). Renaming any
// one of the three breaks the other two silently.

package ygg.daemonlord.madplayer;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.content.IntentFilter;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.media.AudioAttributes;
import android.media.AudioFocusRequest;
import android.media.AudioManager;
import android.media.MediaMetadata;
import android.media.session.MediaSession;
import android.media.session.PlaybackState;
import android.os.Build;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;

public final class PlaybackService extends Service {
    private static final String CHANNEL = "playback";
    private static final int NOTE_ID = 1;
    private static final String EXTRA_CMD = "ygg.daemonlord.madplayer.cmd";

    // The command vocabulary shared with Go. internal/mediasession's
    // service_android.go declares the same numbers; they are the whole
    // protocol, so a change here without one there reorders the buttons.
    public static final int CMD_PLAY = 0;
    public static final int CMD_PAUSE = 1;
    public static final int CMD_NEXT = 2;
    public static final int CMD_PREV = 3;
    public static final int CMD_STOP = 4;
    public static final int CMD_SEEK_MS = 5;

    // Implemented in Go (service_android.go). arg is only meaningful for
    // CMD_SEEK_MS, where it is the absolute target in milliseconds.
    private static native void nativeCommand(int cmd, long arg);

    static {
        // Normally Gio's activity has loaded the library long before this
        // class is touched, and loading again is a no-op. The catch covers the
        // one path where it has not — Android recreating the service in a
        // fresh process — where a plain UnsatisfiedLinkError in a static
        // initialiser would take the whole process down before onCreate.
        try {
            System.loadLibrary("gio");
        } catch (UnsatisfiedLinkError ignored) {
        }
    }

    // Everything below `lock` is the mirror of what Go last pushed. Static,
    // because Go pushes state before and after the service instance exists,
    // and the instance reads whatever is current when Android asks it for a
    // notification.
    private static final Handler main = new Handler(Looper.getMainLooper());
    private static final Object lock = new Object();
    private static PlaybackService instance;
    private static boolean wanted;
    private static String title = "";
    private static String artist = "";
    private static String album = "";
    private static String artPath = "";
    private static long durationMs;
    private static long positionMs;
    private static boolean playing;

    // --- the four entry points Go calls, from its own (attached) thread ------

    // start asks Android for the service. Posted to the main looper like every
    // other mutation here, so the whole class is single-threaded once past the
    // static mirror.
    public static void start(final Context ctx) {
        synchronized (lock) {
            wanted = true;
        }
        main.post(new Runnable() {
            public void run() {
                synchronized (lock) {
                    if (!wanted || instance != null) {
                        return;
                    }
                }
                Intent i = new Intent(ctx, PlaybackService.class);
                if (Build.VERSION.SDK_INT >= 26) {
                    ctx.startForegroundService(i);
                } else {
                    ctx.startService(i);
                }
            }
        });
    }

    public static void setMetadata(String t, String ar, String al, long durMs, String art) {
        synchronized (lock) {
            title = t == null ? "" : t;
            artist = ar == null ? "" : ar;
            album = al == null ? "" : al;
            durationMs = durMs;
            artPath = art == null ? "" : art;
        }
        main.post(new Runnable() {
            public void run() {
                if (instance != null) {
                    instance.apply(true);
                }
            }
        });
    }

    public static void setState(boolean isPlaying, long posMs) {
        final boolean flipped;
        synchronized (lock) {
            flipped = playing != isPlaying;
            playing = isPlaying;
            positionMs = posMs;
        }
        main.post(new Runnable() {
            public void run() {
                if (instance != null) {
                    // The notification is only rebuilt when the play/pause
                    // button has to change face; a position update alone goes
                    // to the session, which clients interpolate from.
                    instance.apply(flipped);
                }
            }
        });
    }

    public static void shutdown() {
        synchronized (lock) {
            wanted = false;
        }
        main.post(new Runnable() {
            public void run() {
                if (instance != null) {
                    instance.stopForeground(true);
                    instance.stopSelf();
                }
            }
        });
    }

    // --- the service itself, main thread only --------------------------------

    private MediaSession session;
    private AudioManager audioman;
    private AudioFocusRequest focusReq;
    private boolean haveFocus;
    private boolean pausedByFocusLoss;
    private BroadcastReceiver noisy;
    private Bitmap art;
    private String artFor = "";

    private final AudioManager.OnAudioFocusChangeListener focusListener =
            new AudioManager.OnAudioFocusChangeListener() {
                public void onAudioFocusChange(int change) {
                    switch (change) {
                        case AudioManager.AUDIOFOCUS_LOSS:
                            // Another player took over for good. Pause and do
                            // not come back on our own — resuming over the top
                            // of somebody's podcast is the classic focus bug.
                            pausedByFocusLoss = false;
                            nativeCommand(CMD_PAUSE, 0);
                            break;
                        case AudioManager.AUDIOFOCUS_LOSS_TRANSIENT:
                        case AudioManager.AUDIOFOCUS_LOSS_TRANSIENT_CAN_DUCK:
                            // A call or a navigation prompt. Ducking is not
                            // implemented (the Go side has one volume and it is
                            // the person's), so both cases pause — and remember
                            // it, because this interruption ends.
                            boolean was;
                            synchronized (lock) {
                                was = playing;
                            }
                            pausedByFocusLoss = was;
                            if (was) {
                                nativeCommand(CMD_PAUSE, 0);
                            }
                            break;
                        case AudioManager.AUDIOFOCUS_GAIN:
                            if (pausedByFocusLoss) {
                                pausedByFocusLoss = false;
                                nativeCommand(CMD_PLAY, 0);
                            }
                            break;
                    }
                }
            };

    @Override
    public void onCreate() {
        super.onCreate();
        audioman = (AudioManager) getSystemService(Context.AUDIO_SERVICE);
        if (Build.VERSION.SDK_INT >= 26) {
            NotificationChannel ch =
                    new NotificationChannel(CHANNEL, "Playback", NotificationManager.IMPORTANCE_LOW);
            ch.setShowBadge(false);
            ((NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE))
                    .createNotificationChannel(ch);
        }

        session = new MediaSession(this, "madplayer");
        session.setCallback(new MediaSession.Callback() {
            @Override
            public void onPlay() {
                nativeCommand(CMD_PLAY, 0);
            }

            @Override
            public void onPause() {
                nativeCommand(CMD_PAUSE, 0);
            }

            @Override
            public void onSkipToNext() {
                nativeCommand(CMD_NEXT, 0);
            }

            @Override
            public void onSkipToPrevious() {
                nativeCommand(CMD_PREV, 0);
            }

            @Override
            public void onStop() {
                nativeCommand(CMD_STOP, 0);
            }

            @Override
            public void onSeekTo(long pos) {
                nativeCommand(CMD_SEEK_MS, pos);
            }
        });
        session.setActive(true);

        // Headphones unplugged: Android routes the audio to the speaker and
        // broadcasts this. Every music player pauses; not pausing plays the
        // quiet carriage your music.
        noisy = new BroadcastReceiver() {
            @Override
            public void onReceive(Context c, Intent i) {
                boolean was;
                synchronized (lock) {
                    was = playing;
                }
                if (was) {
                    nativeCommand(CMD_PAUSE, 0);
                }
            }
        };
        registerReceiver(noisy, new IntentFilter(AudioManager.ACTION_AUDIO_BECOMING_NOISY));

        synchronized (lock) {
            instance = this;
        }
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        // Every start lands here: the initial promotion to foreground, and
        // each notification button press (their PendingIntents are
        // getService). startForeground must run within seconds of
        // startForegroundService or Android kills the app, so it is
        // unconditional and first.
        startForeground(NOTE_ID, buildNotification());
        apply(false);
        if (intent != null) {
            int cmd = intent.getIntExtra(EXTRA_CMD, -1);
            if (cmd >= 0) {
                nativeCommand(cmd, 0);
            }
        }
        // NOT_STICKY: a restart with no Go side would be a notification
        // controlling nothing. Go starts the service again when it has state.
        return START_NOT_STICKY;
    }

    @Override
    public void onTaskRemoved(Intent rootIntent) {
        // The person swiped the app away. Playing on is the point of a music
        // player; but a PAUSED one lingering in the shade after the app was
        // visibly closed reads as a bug, so that one goes.
        boolean keep;
        synchronized (lock) {
            keep = playing;
        }
        if (!keep) {
            stopForeground(true);
            stopSelf();
        }
    }

    @Override
    public void onDestroy() {
        synchronized (lock) {
            instance = null;
        }
        if (noisy != null) {
            unregisterReceiver(noisy);
            noisy = null;
        }
        dropFocus();
        session.setActive(false);
        session.release();
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    // apply pushes the mirror into the session, and rebuilds the notification
    // when its face changed. Main thread only.
    private void apply(boolean renotify) {
        String t, ar, al, artp;
        long dur, pos;
        boolean play;
        synchronized (lock) {
            t = title;
            ar = artist;
            al = album;
            artp = artPath;
            dur = durationMs;
            pos = positionMs;
            play = playing;
        }

        if (!artp.equals(artFor)) {
            // Decoded once per path, not per update: the same cover arrives
            // with every state push.
            artFor = artp;
            art = artp.isEmpty() ? null : BitmapFactory.decodeFile(artp);
            renotify = true;
        }

        MediaMetadata.Builder md = new MediaMetadata.Builder()
                .putString(MediaMetadata.METADATA_KEY_TITLE, t)
                .putString(MediaMetadata.METADATA_KEY_ARTIST, ar)
                .putString(MediaMetadata.METADATA_KEY_ALBUM, al);
        if (dur > 0) {
            md.putLong(MediaMetadata.METADATA_KEY_DURATION, dur);
        }
        if (art != null) {
            md.putBitmap(MediaMetadata.METADATA_KEY_ALBUM_ART, art);
        }
        session.setMetadata(md.build());

        // Position is stamped once with a speed; clients interpolate from the
        // stamp rather than being fed a stream of updates. Android 13+ also
        // derives the carousel's buttons from these action bits, so they are
        // the notification actions' modern twin, not decoration.
        session.setPlaybackState(new PlaybackState.Builder()
                .setActions(PlaybackState.ACTION_PLAY | PlaybackState.ACTION_PAUSE
                        | PlaybackState.ACTION_PLAY_PAUSE | PlaybackState.ACTION_STOP
                        | PlaybackState.ACTION_SKIP_TO_NEXT | PlaybackState.ACTION_SKIP_TO_PREVIOUS
                        | PlaybackState.ACTION_SEEK_TO)
                .setState(play ? PlaybackState.STATE_PLAYING : PlaybackState.STATE_PAUSED,
                        pos, play ? 1f : 0f)
                .build());

        if (play) {
            takeFocus();
        }
        if (renotify) {
            ((NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE))
                    .notify(NOTE_ID, buildNotification());
        }
    }

    private void takeFocus() {
        if (haveFocus) {
            return;
        }
        int r;
        if (Build.VERSION.SDK_INT >= 26) {
            if (focusReq == null) {
                focusReq = new AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN)
                        .setAudioAttributes(new AudioAttributes.Builder()
                                .setUsage(AudioAttributes.USAGE_MEDIA)
                                .setContentType(AudioAttributes.CONTENT_TYPE_MUSIC)
                                .build())
                        .setOnAudioFocusChangeListener(focusListener)
                        .build();
            }
            r = audioman.requestAudioFocus(focusReq);
        } else {
            r = audioman.requestAudioFocus(focusListener, AudioManager.STREAM_MUSIC,
                    AudioManager.AUDIOFOCUS_GAIN);
        }
        haveFocus = r == AudioManager.AUDIOFOCUS_REQUEST_GRANTED;
    }

    private void dropFocus() {
        if (!haveFocus) {
            return;
        }
        haveFocus = false;
        if (Build.VERSION.SDK_INT >= 26) {
            if (focusReq != null) {
                audioman.abandonAudioFocusRequest(focusReq);
            }
        } else {
            audioman.abandonAudioFocus(focusListener);
        }
    }

    private Notification buildNotification() {
        String t, ar;
        boolean play;
        synchronized (lock) {
            t = title;
            ar = artist;
            play = playing;
        }

        Notification.Builder b = Build.VERSION.SDK_INT >= 26
                ? new Notification.Builder(this, CHANNEL)
                : new Notification.Builder(this);

        // The launcher icon doubles as the status-bar icon. It is a resource
        // id straight off ApplicationInfo, so there is nothing to look up by
        // name and nothing for a rename to break; the media glyph is the
        // fallback for the impossible case of an APK with no icon.
        int icon = getApplicationInfo().icon;
        b.setSmallIcon(icon != 0 ? icon : android.R.drawable.ic_media_play);
        b.setContentTitle(t.isEmpty() ? "madplayer" : t);
        if (!ar.isEmpty()) {
            b.setContentText(ar);
        }
        if (art != null) {
            b.setLargeIcon(art);
        }
        b.setOngoing(play);
        b.setVisibility(Notification.VISIBILITY_PUBLIC);
        b.setOnlyAlertOnce(true);

        // Tapping the body reopens the player. The launch intent rather than a
        // hard-coded activity class: this jar is compiled without Gio's
        // classes on the classpath, and the package manager knows the answer
        // anyway.
        Intent open = getPackageManager().getLaunchIntentForPackage(getPackageName());
        if (open != null) {
            b.setContentIntent(PendingIntent.getActivity(this, 0, open, pendingFlags(0)));
        }

        b.addAction(action(android.R.drawable.ic_media_previous, "Previous", CMD_PREV));
        if (play) {
            b.addAction(action(android.R.drawable.ic_media_pause, "Pause", CMD_PAUSE));
        } else {
            b.addAction(action(android.R.drawable.ic_media_play, "Play", CMD_PLAY));
        }
        b.addAction(action(android.R.drawable.ic_media_next, "Next", CMD_NEXT));

        b.setStyle(new Notification.MediaStyle()
                .setMediaSession(session.getSessionToken())
                .setShowActionsInCompactView(0, 1, 2));
        return b.build();
    }

    private Notification.Action action(int icon, String label, int cmd) {
        Intent i = new Intent(this, PlaybackService.class).putExtra(EXTRA_CMD, cmd);
        // One request code per command, or the PendingIntents collapse into
        // whichever extra was filed first — three buttons, one action.
        PendingIntent pi = PendingIntent.getService(this, cmd, i,
                pendingFlags(PendingIntent.FLAG_UPDATE_CURRENT));
        return new Notification.Action.Builder(icon, label, pi).build();
    }

    private static int pendingFlags(int base) {
        // Mutability must be declared from 31 on; IMMUTABLE exists from 23.
        return Build.VERSION.SDK_INT >= 23 ? base | PendingIntent.FLAG_IMMUTABLE : base;
    }
}
