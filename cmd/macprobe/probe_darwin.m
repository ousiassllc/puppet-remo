//go:build darwin

// macOS 実機の成立性プローブ（Issue #186）の Objective-C 側。製品には含めない。
// 各 mp_* は main スレッドから呼ぶ（Go 側が runtime.LockOSThread で保証する）。結果は JSON 文字列で返す。
// 守ること: 撮るのはディスプレイとプローブ自身の窓だけで、結果には他アプリの窓タイトル・内容を書かない（数だけ）。
// 入力はプローブ自身の窓だけに向ける。画面ロック中はグローバル入力（HID タップ）を一切送らない。許可の回避はしない。

#import <Cocoa/Cocoa.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <ApplicationServices/ApplicationServices.h>
#import <ImageIO/ImageIO.h>
#import <Security/Security.h>
#include <poll.h>
#include <sys/sysctl.h>
#include <sys/utsname.h>
#include <unistd.h>
#include "probe_darwin.h"

#pragma mark - 共通

// Boolean（unsigned char）や int を JSON の true/false にするため、必ず BOOL へ畳む。
#define MPB(x) ((BOOL)((x) ? YES : NO))

static char *mpJSON(id obj) {
	NSData *d = [NSJSONSerialization dataWithJSONObject:obj
												options:NSJSONWritingSortedKeys | NSJSONWritingPrettyPrinted
												  error:nil];
	NSString *s = [[NSString alloc] initWithData:d encoding:NSUTF8StringEncoding];
	return strdup(s.UTF8String ?: "{}");
}

void mp_free(char *p) { free(p); }

static NSString *errStr(NSError *e) {
	return e ? [NSString stringWithFormat:@"%@ (domain=%@ code=%ld)", e.localizedDescription, e.domain, (long)e.code] : nil;
}

static NSDictionary *rectDict(CGRect r) {
	return @{@"x": @(r.origin.x), @"y": @(r.origin.y), @"w": @(r.size.width), @"h": @(r.size.height)};
}

static void initApp(void) {
	static dispatch_once_t once;
	dispatch_once(&once, ^{
		[NSApplication sharedApplication];
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
		[NSApp finishLaunching];
	});
}

// main run loop を回しつつ NSEvent を窓へ配る（[NSApp run] を使わず、同期的に待つため）。
static void pumpApp(NSTimeInterval sec) {
	NSDate *end = [NSDate dateWithTimeIntervalSinceNow:sec];
	while ([end timeIntervalSinceNow] > 0) {
		@autoreleasepool {
			NSEvent *e = [NSApp nextEventMatchingMask:NSEventMaskAny
											untilDate:[NSDate dateWithTimeIntervalSinceNow:0.01]
											   inMode:NSDefaultRunLoopMode
											  dequeue:YES];
			if (e) [NSApp sendEvent:e];
			[NSApp updateWindows];
		}
	}
}

static BOOL waitFor(BOOL (^cond)(void), NSTimeInterval timeout) {
	NSDate *end = [NSDate dateWithTimeIntervalSinceNow:timeout];
	while (!cond() && [end timeIntervalSinceNow] > 0) pumpApp(0.02);
	return cond();
}

static BOOL savePNG(CGImageRef img, NSString *path) {
	CGImageDestinationRef dst = CGImageDestinationCreateWithURL((__bridge CFURLRef)[NSURL fileURLWithPath:path], CFSTR("public.png"), 1, NULL);
	if (!dst) return NO;
	CGImageDestinationAddImage(dst, img, NULL);
	BOOL ok = CGImageDestinationFinalize(dst);
	CFRelease(dst);
	return ok;
}

static NSString *sysctlString(const char *name) {
	size_t n = 0;
	if (sysctlbyname(name, NULL, &n, NULL, 0) != 0 || n == 0) return @"";
	char *buf = malloc(n);
	NSString *s = @"";
	if (sysctlbyname(name, buf, &n, NULL, 0) == 0) s = [NSString stringWithUTF8String:buf] ?: @"";
	free(buf);
	return s;
}

static SCShareableContent *fetchContent(NSError **errOut) {
	__block SCShareableContent *c = nil;
	__block NSError *e = nil;
	__block BOOL done = NO;
	[SCShareableContent getShareableContentExcludingDesktopWindows:NO
											   onScreenWindowsOnly:NO
												 completionHandler:^(SCShareableContent *content, NSError *error) {
													 c = content;
													 e = error;
													 done = YES;
												 }];
	if (!waitFor(^BOOL { return done; }, 15) && !e) e = [NSError errorWithDomain:@"macprobe" code:1 userInfo:@{NSLocalizedDescriptionKey: @"SCShareableContent timeout"}];
	if (errOut) *errOut = e;
	return c;
}

static BOOL sessionLocked(void) {
	CFDictionaryRef s = CGSessionCopyCurrentDictionary();
	if (!s) return NO;
	NSDictionary *d = (__bridge_transfer NSDictionary *)s;
	return [d[@"CGSSessionScreenIsLocked"] boolValue];
}

// 窓を作る共通部。borderless で通常は click 透過。
static NSWindow *makeWindow(NSRect frame, NSString *title, NSWindowStyleMask style) {
	NSWindow *w = [[NSWindow alloc] initWithContentRect:frame styleMask:style backing:NSBackingStoreBuffered defer:NO];
	w.releasedWhenClosed = NO;
	w.title = title ?: @"";
	return w;
}

static NSView *colorView(NSRect frame, NSColor *c) {
	NSView *v = [[NSView alloc] initWithFrame:frame];
	v.wantsLayer = YES;
	v.layer.backgroundColor = c.CGColor;
	return v;
}

// Cocoa の画面座標（左下原点）→ ディスプレイ内のピクセル矩形（左上原点）。
static CGRect pxRect(NSRect cocoa, CGRect displayFrame, CGFloat scale) {
	CGFloat primaryH = [NSScreen screens].firstObject.frame.size.height;
	CGFloat topY = primaryH - NSMaxY(cocoa);
	return CGRectMake((cocoa.origin.x - displayFrame.origin.x) * scale, (topY - displayFrame.origin.y) * scale,
					  cocoa.size.width * scale, cocoa.size.height * scale);
}

#pragma mark - status（許可・環境）

static NSDictionary *signatureInfo(void) {
	NSMutableDictionary *d = [NSMutableDictionary dictionary];
	SecCodeRef self = NULL;
	if (SecCodeCopySelf(kSecCSDefaultFlags, &self) != errSecSuccess || !self) return @{@"error": @"SecCodeCopySelf"};
	SecStaticCodeRef st = NULL;
	if (SecCodeCopyStaticCode(self, kSecCSDefaultFlags, &st) == errSecSuccess && st) {
		CFDictionaryRef info = NULL;
		if (SecCodeCopySigningInformation(st, kSecCSSigningInformation, &info) == errSecSuccess && info) {
			NSDictionary *i = (__bridge_transfer NSDictionary *)info;
			d[@"identifier"] = i[(__bridge NSString *)kSecCodeInfoIdentifier] ?: @"";
			d[@"flags"] = i[(__bridge NSString *)kSecCodeInfoFlags] ?: @0;
			d[@"adhoc"] = @(MPB(([i[(__bridge NSString *)kSecCodeInfoFlags] unsignedIntValue] & kSecCodeSignatureAdhoc) != 0));
			d[@"team_id"] = i[(__bridge NSString *)kSecCodeInfoTeamIdentifier] ?: @"";
			NSData *u = i[(__bridge NSString *)kSecCodeInfoUnique];
			if (u) {
				NSMutableString *hex = [NSMutableString string];
				const unsigned char *b = u.bytes;
				for (NSUInteger k = 0; k < u.length; k++) [hex appendFormat:@"%02x", b[k]];
				d[@"cdhash"] = hex;
			}
		}
		CFRelease(st);
	}
	CFRelease(self);
	return d;
}

static NSDictionary *statusDict(void) {
	initApp();
	NSMutableDictionary *d = [NSMutableDictionary dictionary];
	d[@"screen_capture_preflight"] = @(MPB(CGPreflightScreenCaptureAccess()));
	d[@"accessibility_trusted"] = @(MPB(AXIsProcessTrusted()));
	d[@"input_monitoring_preflight"] = @(MPB(CGPreflightListenEventAccess()));
	d[@"post_event_preflight"] = @(MPB(CGPreflightPostEventAccess()));
	d[@"screen_locked"] = @(MPB(sessionLocked()));
	CFDictionaryRef sess = CGSessionCopyCurrentDictionary();
	if (sess) {
		NSDictionary *s = (__bridge_transfer NSDictionary *)sess;
		d[@"on_console"] = s[@"kCGSSessionOnConsoleKey"] ?: @NO;
		d[@"login_done"] = s[@"kCGSessionLoginDoneKey"] ?: @NO;
	}
	NSOperatingSystemVersion v = [NSProcessInfo processInfo].operatingSystemVersion;
	d[@"macos_version"] = [NSString stringWithFormat:@"%ld.%ld.%ld", (long)v.majorVersion, (long)v.minorVersion, (long)v.patchVersion];
	d[@"macos_build"] = sysctlString("kern.osversion");
	struct utsname u;
	uname(&u);
	d[@"machine"] = [NSString stringWithUTF8String:u.machine];
	d[@"model"] = sysctlString("hw.model");
	d[@"cpu_brand"] = sysctlString("machdep.cpu.brand_string");
	d[@"pid"] = @(getpid());
	d[@"ppid"] = @(getppid());
	d[@"launched_from_ssh"] = @(MPB(getenv("SSH_CONNECTION") != NULL));
	d[@"executable"] = [NSBundle mainBundle].executablePath ?: @"";
	d[@"bundle_path"] = [NSBundle mainBundle].bundlePath ?: @"";
	d[@"bundle_id"] = [NSBundle mainBundle].bundleIdentifier ?: @"";
	d[@"signature"] = signatureInfo();
	NSMutableArray *screens = [NSMutableArray array];
	for (NSScreen *s in [NSScreen screens]) {
		CGDirectDisplayID id = [s.deviceDescription[@"NSScreenNumber"] unsignedIntValue];
		[screens addObject:@{
			@"display_id": @(id),
			@"frame_points": rectDict(s.frame),
			@"backing_scale_factor": @(s.backingScaleFactor),
			@"pixels_wide": @(CGDisplayPixelsWide(id)),
			@"pixels_high": @(CGDisplayPixelsHigh(id)),
			@"is_main": @(MPB(id == CGMainDisplayID())),
			@"builtin": @(MPB(CGDisplayIsBuiltin(id) != 0)),
		}];
	}
	d[@"screens"] = screens;
	return d;
}

char *mp_status(void) { return mpJSON(statusDict()); }

char *mp_request(void) {
	initApp();
	NSMutableDictionary *r = [NSMutableDictionary dictionary];
	r[@"before"] = @{@"screen_capture": @(MPB(CGPreflightScreenCaptureAccess())), @"accessibility": @(MPB(AXIsProcessTrusted()))};
	r[@"screen_capture_request_returned"] = @(MPB(CGRequestScreenCaptureAccess()));
	NSDictionary *opt = @{(__bridge NSString *)kAXTrustedCheckOptionPrompt: @YES};
	r[@"accessibility_prompt_returned"] = @(MPB(AXIsProcessTrustedWithOptions((__bridge CFDictionaryRef)opt)));
	pumpApp(1.0);
	r[@"after"] = @{@"screen_capture": @(MPB(CGPreflightScreenCaptureAccess())), @"accessibility": @(MPB(AXIsProcessTrusted()))};
	return mpJSON(r);
}

#pragma mark - windows（項目 5）

static BOOL hasName(id n) { return [n isKindOfClass:[NSString class]] && [(NSString *)n length] > 0; }

char *mp_windows(int fixturePID, const char *fixtureTitle) {
	initApp();
	NSString *fixTitle = [NSString stringWithUTF8String:fixtureTitle];
	// 自プロセスの窓（比較用）。
	NSWindow *own = makeWindow(NSMakeRect(900, 300, 200, 100), @"macprobe-own-title", NSWindowStyleMaskTitled);
	[own orderFrontRegardless];
	pumpApp(0.5);

	NSMutableDictionary *r = [NSMutableDictionary dictionary];
	r[@"status"] = statusDict();
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionAll, kCGNullWindowID);
	NSArray *wins = (__bridge_transfer NSArray *)list;
	pid_t me = getpid();
	NSInteger total = wins.count, ownN = 0, ownNamed = 0, fixN = 0, fixNamed = 0, fixNameMatch = 0;
	NSInteger otherN = 0, otherOwnerName = 0, otherNamed = 0, otherLayer0 = 0, otherLayer0Named = 0, otherOnscreenNamed = 0;
	for (NSDictionary *w in wins) {
		pid_t pid = [w[(__bridge NSString *)kCGWindowOwnerPID] intValue];
		id name = w[(__bridge NSString *)kCGWindowName];
		NSInteger layer = [w[(__bridge NSString *)kCGWindowLayer] integerValue];
		BOOL onscreen = [w[(__bridge NSString *)kCGWindowIsOnscreen] boolValue];
		if (pid == me) {
			ownN++;
			if (hasName(name)) ownNamed++;
		} else if (pid == fixturePID) {
			fixN++;
			if (hasName(name)) fixNamed++;
			if ([name isEqual:fixTitle]) fixNameMatch++;
		} else {
			otherN++;
			if (hasName(w[(__bridge NSString *)kCGWindowOwnerName])) otherOwnerName++;
			if (hasName(name)) otherNamed++;
			if (layer == 0) {
				otherLayer0++;
				if (hasName(name)) otherLayer0Named++;
			}
			if (onscreen && hasName(name)) otherOnscreenNamed++;
		}
	}
	r[@"cgwindowlist"] = @{
		@"total": @(total),
		@"own_windows": @(ownN), @"own_with_kCGWindowName": @(ownNamed),
		@"fixture_windows": @(fixN), @"fixture_with_kCGWindowName": @(fixNamed), @"fixture_name_matches": @(fixNameMatch),
		@"other_windows": @(otherN), @"other_with_kCGWindowOwnerName": @(otherOwnerName),
		@"other_with_kCGWindowName": @(otherNamed), @"other_layer0": @(otherLayer0),
		@"other_layer0_with_kCGWindowName": @(otherLayer0Named), @"other_onscreen_with_kCGWindowName": @(otherOnscreenNamed),
	};

	NSError *err = nil;
	SCShareableContent *c = fetchContent(&err);
	NSMutableDictionary *sck = [NSMutableDictionary dictionary];
	if (c) {
		NSInteger scFix = 0, scFixTitled = 0, scOtherTitled = 0, scOther = 0, scOwnTitled = 0;
		for (SCWindow *w in c.windows) {
			pid_t pid = w.owningApplication.processID;
			BOOL titled = w.title.length > 0;
			if (pid == fixturePID) {
				scFix++;
				if ([w.title isEqual:fixTitle]) scFixTitled++;
			} else if (pid == me) {
				if (titled) scOwnTitled++;
			} else {
				scOther++;
				if (titled) scOtherTitled++;
			}
		}
		sck[@"windows_total"] = @(c.windows.count);
		sck[@"fixture_windows"] = @(scFix);
		sck[@"fixture_title_matches"] = @(scFixTitled);
		sck[@"own_titled"] = @(scOwnTitled);
		sck[@"other_windows"] = @(scOther);
		sck[@"other_with_title"] = @(scOtherTitled);
	} else {
		sck[@"error"] = errStr(err) ?: @"nil content";
	}
	r[@"scshareablecontent"] = sck;
	[own close];
	return mpJSON(r);
}

#pragma mark - fixture（別プロセスのテスト窓）

int mp_fixture(const char *titleC, double seconds) {
	initApp();
	NSWindow *w = makeWindow(NSMakeRect(900, 500, 200, 100), [NSString stringWithUTF8String:titleC], NSWindowStyleMaskTitled);
	[w orderFrontRegardless];
	pumpApp(0.3);
	printf("READY %d %ld\n", getpid(), (long)w.windowNumber);
	fflush(stdout);
	NSDate *end = [NSDate dateWithTimeIntervalSinceNow:seconds];
	while ([end timeIntervalSinceNow] > 0) {
		pumpApp(0.1);
		struct pollfd p = {.fd = 0, .events = POLLIN};
		if (poll(&p, 1, 0) > 0) {
			char b[64];
			if (read(0, b, sizeof b) <= 0) break; // 親が閉じた
		}
	}
	return 0;
}

#pragma mark - capture（項目 1）

static CGImageRef captureShot(SCContentFilter *f, size_t w, size_t h, NSError **errOut) {
	SCStreamConfiguration *cfg = [[SCStreamConfiguration alloc] init];
	cfg.width = w;
	cfg.height = h;
	cfg.pixelFormat = kCVPixelFormatType_32BGRA;
	cfg.showsCursor = NO;
	__block CGImageRef img = NULL;
	__block NSError *e = nil;
	__block BOOL done = NO;
	[SCScreenshotManager captureImageWithFilter:f
								  configuration:cfg
							  completionHandler:^(CGImageRef image, NSError *error) {
								  if (image) img = CGImageRetain(image);
								  e = error;
								  done = YES;
							  }];
	if (!waitFor(^BOOL { return done; }, 15) && !e) e = [NSError errorWithDomain:@"macprobe" code:2 userInfo:@{NSLocalizedDescriptionKey: @"captureImage timeout"}];
	if (errOut) *errOut = e;
	return img; // 呼び出し側が CGImageRelease
}

@interface MPSink : NSObject <SCStreamOutput, SCStreamDelegate>
@property (nonatomic) NSInteger frames, complete, idle, blank, other, withDirty, maxRects;
@property (nonatomic) double dirtyAreaSum, firstWall, lastWall;
@property (nonatomic) CGRect dirtyUnion;
@property (nonatomic) BOOL unionSet;
@property (nonatomic) size_t width, height;
@property (nonatomic) NSMutableArray *firstDirty;
@property (nonatomic) NSMutableDictionary *frameInfo;
@property (nonatomic) NSString *stopError;
@end

@implementation MPSink
- (instancetype)init {
	if ((self = [super init])) {
		_firstDirty = [NSMutableArray array];
		_frameInfo = [NSMutableDictionary dictionary];
		_dirtyUnion = CGRectNull;
	}
	return self;
}
- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
	@synchronized(self) { self.stopError = errStr(error); }
}
- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sb ofType:(SCStreamOutputType)type {
	if (type != SCStreamOutputTypeScreen || !CMSampleBufferIsValid(sb)) return;
	CFArrayRef atts = CMSampleBufferGetSampleAttachmentsArray(sb, false);
	if (!atts || CFArrayGetCount(atts) == 0) return;
	NSDictionary *a = (__bridge NSDictionary *)CFArrayGetValueAtIndex(atts, 0);
	@synchronized(self) {
		double now = CFAbsoluteTimeGetCurrent();
		if (self.frames == 0) self.firstWall = now;
		self.lastWall = now;
		self.frames++;
		NSNumber *st = a[SCStreamFrameInfoStatus];
		SCFrameStatus s = st ? (SCFrameStatus)st.integerValue : SCFrameStatusBlank;
		if (s == SCFrameStatusComplete) self.complete++;
		else if (s == SCFrameStatusIdle) self.idle++;
		else if (s == SCFrameStatusBlank) self.blank++;
		else self.other++;
		CVImageBufferRef ib = CMSampleBufferGetImageBuffer(sb);
		if (ib) {
			self.width = CVPixelBufferGetWidth(ib);
			self.height = CVPixelBufferGetHeight(ib);
		}
		if (s == SCFrameStatusComplete && self.frameInfo.count == 0) {
			CGRect cr = CGRectZero;
			NSDictionary *crd = a[SCStreamFrameInfoContentRect];
			if (crd) CGRectMakeWithDictionaryRepresentation((__bridge CFDictionaryRef)crd, &cr);
			self.frameInfo[@"content_rect"] = rectDict(cr);
			self.frameInfo[@"content_scale"] = a[SCStreamFrameInfoContentScale] ?: [NSNull null];
			self.frameInfo[@"scale_factor"] = a[SCStreamFrameInfoScaleFactor] ?: [NSNull null];
		}
		NSArray *dr = a[SCStreamFrameInfoDirtyRects];
		if (dr.count > 0 && s == SCFrameStatusComplete) {
			self.withDirty++;
			if ((NSInteger)dr.count > self.maxRects) self.maxRects = dr.count;
			NSMutableArray *rects = [NSMutableArray array];
			for (NSDictionary *rd in dr) {
				CGRect r;
				if (!CGRectMakeWithDictionaryRepresentation((__bridge CFDictionaryRef)rd, &r)) continue;
				self.dirtyAreaSum += r.size.width * r.size.height;
				self.dirtyUnion = self.unionSet ? CGRectUnion(self.dirtyUnion, r) : r;
				self.unionSet = YES;
				[rects addObject:rectDict(r)];
			}
			if (self.firstDirty.count < 3) [self.firstDirty addObject:rects];
		}
	}
}
@end

// 動く箱。dirtyRects を確かめるために、画面の一部を毎フレーム塗り替える。
@interface MPAnim : NSObject
@property (nonatomic) NSWindow *win;
@property (nonatomic) NSView *box;
@property (nonatomic) NSTimer *timer;
@property (nonatomic) NSInteger tick;
@end
@implementation MPAnim
- (instancetype)initWithFrame:(NSRect)f {
	if ((self = [super init])) {
		_win = makeWindow(f, @"macprobe-anim", NSWindowStyleMaskBorderless);
		_win.level = NSStatusWindowLevel;
		_win.ignoresMouseEvents = YES;
		_win.hasShadow = NO;
		_win.contentView = colorView(NSMakeRect(0, 0, f.size.width, f.size.height), NSColor.blackColor);
		_box = _win.contentView;
		[_win orderFrontRegardless];
	}
	return self;
}
- (void)start {
	__weak MPAnim *w = self;
	self.timer = [NSTimer scheduledTimerWithTimeInterval:1.0 / 30 repeats:YES block:^(NSTimer *t) {
		MPAnim *s = w;
		if (!s) return;
		s.tick++;
		CGFloat h = (s.tick % 60) / 60.0;
		s.box.layer.backgroundColor = [NSColor colorWithHue:h saturation:1 brightness:1 alpha:1].CGColor;
	}];
}
- (void)stop {
	[self.timer invalidate];
	self.timer = nil;
}
@end

static NSDictionary *runStream(SCContentFilter *f, size_t w, size_t h, double seconds, NSString *label) {
	NSMutableDictionary *r = [NSMutableDictionary dictionary];
	r[@"label"] = label;
	MPSink *sink = [[MPSink alloc] init];
	SCStreamConfiguration *cfg = [[SCStreamConfiguration alloc] init];
	cfg.width = w;
	cfg.height = h;
	cfg.pixelFormat = kCVPixelFormatType_32BGRA;
	cfg.showsCursor = NO;
	cfg.queueDepth = 4;
	cfg.minimumFrameInterval = CMTimeMake(1, 30);
	SCStream *stream = [[SCStream alloc] initWithFilter:f configuration:cfg delegate:sink];
	NSError *err = nil;
	dispatch_queue_t q = dispatch_queue_create("macprobe.stream", DISPATCH_QUEUE_SERIAL);
	if (![stream addStreamOutput:sink type:SCStreamOutputTypeScreen sampleHandlerQueue:q error:&err]) {
		r[@"error"] = [@"addStreamOutput: " stringByAppendingString:errStr(err) ?: @""];
		return r;
	}
	__block BOOL started = NO;
	__block NSError *startErr = nil;
	[stream startCaptureWithCompletionHandler:^(NSError *e) {
		startErr = e;
		started = YES;
	}];
	if (!waitFor(^BOOL { return started; }, 15)) {
		r[@"error"] = @"startCapture timeout";
		return r;
	}
	if (startErr) {
		r[@"error"] = [@"startCapture: " stringByAppendingString:errStr(startErr)];
		return r;
	}
	pumpApp(seconds);
	__block BOOL stopped = NO;
	[stream stopCaptureWithCompletionHandler:^(NSError *e) { stopped = YES; }];
	waitFor(^BOOL { return stopped; }, 10);
	@synchronized(sink) {
		double span = sink.lastWall - sink.firstWall;
		r[@"seconds"] = @(seconds);
		r[@"frames"] = @(sink.frames);
		r[@"complete"] = @(sink.complete);
		r[@"idle"] = @(sink.idle);
		r[@"blank"] = @(sink.blank);
		r[@"other_status"] = @(sink.other);
		r[@"fps_observed"] = @(span > 0 ? (sink.frames - 1) / span : 0);
		r[@"frames_with_dirty_rects"] = @(sink.withDirty);
		r[@"max_dirty_rects_per_frame"] = @(sink.maxRects);
		r[@"buffer_size_px"] = @{@"w": @(sink.width), @"h": @(sink.height)};
		r[@"dirty_area_px_sum"] = @(sink.dirtyAreaSum);
		if (sink.unionSet) r[@"dirty_union_px"] = rectDict(sink.dirtyUnion);
		r[@"first_dirty_rects"] = sink.firstDirty;
		r[@"frame_info"] = sink.frameInfo;
		if (sink.stopError) r[@"stream_error"] = sink.stopError;
	}
	return r;
}

char *mp_capture(const char *outDirC, double seconds) {
	initApp();
	NSString *outDir = [NSString stringWithUTF8String:outDirC];
	NSMutableDictionary *r = [NSMutableDictionary dictionary];
	r[@"status"] = statusDict();
	NSError *err = nil;
	SCShareableContent *c = fetchContent(&err);
	if (!c) {
		r[@"error"] = errStr(err) ?: @"nil content";
		return mpJSON(r);
	}
	NSMutableArray *displays = [NSMutableArray array];
	for (SCDisplay *d in c.displays) {
		SCContentFilter *f = [[SCContentFilter alloc] initWithDisplay:d excludingWindows:@[]];
		SCShareableContentInfo *info = [SCShareableContent infoForFilter:f];
		[displays addObject:@{
			@"display_id": @(d.displayID),
			@"frame_points": rectDict(d.frame),
			@"width_points": @(d.width), @"height_points": @(d.height),
			@"point_pixel_scale": @(info.pointPixelScale),
			@"content_rect": rectDict(info.contentRect),
		}];
	}
	r[@"displays"] = displays;
	r[@"windows_total"] = @(c.windows.count);
	r[@"applications_total"] = @(c.applications.count);

	SCDisplay *main = nil;
	for (SCDisplay *d in c.displays) if (d.displayID == CGMainDisplayID()) main = d;
	if (!main) main = c.displays.firstObject;
	if (!main) {
		r[@"error"] = @"no display";
		return mpJSON(r);
	}
	SCContentFilter *full = [[SCContentFilter alloc] initWithDisplay:main excludingWindows:@[]];
	CGFloat scale = [SCShareableContent infoForFilter:full].pointPixelScale;
	size_t pw = (size_t)(main.width * scale), ph = (size_t)(main.height * scale);

	// 全ディスプレイを 1 枚ずつ撮る（複数台・Retina の検証。今回の機材は 1 台）。
	NSMutableArray *stills = [NSMutableArray array];
	for (SCDisplay *d in c.displays) {
		SCContentFilter *f = [[SCContentFilter alloc] initWithDisplay:d excludingWindows:@[]];
		CGFloat sc = [SCShareableContent infoForFilter:f].pointPixelScale;
		NSError *e = nil;
		CGImageRef img = captureShot(f, (size_t)(d.width * sc), (size_t)(d.height * sc), &e);
		NSMutableDictionary *s = [NSMutableDictionary dictionaryWithDictionary:@{@"display_id": @(d.displayID), @"requested_px": @{@"w": @((size_t)(d.width * sc)), @"h": @((size_t)(d.height * sc))}}];
		if (img) {
			NSString *p = [outDir stringByAppendingPathComponent:[NSString stringWithFormat:@"still-display-%u.png", d.displayID]];
			s[@"file"] = p.lastPathComponent;
			s[@"saved"] = @(savePNG(img, p));
			s[@"image_px"] = @{@"w": @(CGImageGetWidth(img)), @"h": @(CGImageGetHeight(img))};
			CGImageRelease(img);
		} else {
			s[@"error"] = errStr(e) ?: @"nil image";
		}
		[stills addObject:s];
	}
	r[@"stills"] = stills;

	// 継続キャプチャ。動く箱でフレームごとに画面の一部を変える。
	NSRect animCocoa = NSMakeRect(main.frame.origin.x + 300, 300, 320, 200);
	MPAnim *anim = [[MPAnim alloc] initWithFrame:animCocoa];
	pumpApp(0.5);
	r[@"anim_rect_px"] = rectDict(pxRect(animCocoa, main.frame, scale));
	NSMutableArray *streams = [NSMutableArray array];
	[anim start];
	[streams addObject:runStream(full, pw, ph, seconds, @"display_all_animating")];
	[anim stop];
	[streams addObject:runStream(full, pw, ph, MAX(1.5, seconds / 2), @"display_all_static")];
	// 動く箱の窓だけを合成した配信（ほかの窓の動きが混ざらない対照）。
	SCWindow *animSC = nil;
	for (int i = 0; i < 20 && !animSC; i++) {
		NSError *e = nil;
		SCShareableContent *c2 = fetchContent(&e);
		for (SCWindow *w in c2.windows) if (w.windowID == (CGWindowID)anim.win.windowNumber) animSC = w;
		if (!animSC) pumpApp(0.2);
	}
	if (animSC) {
		[anim start];
		SCContentFilter *wf = [[SCContentFilter alloc] initWithDisplay:main includingWindows:@[animSC]];
		[streams addObject:runStream(wf, pw, ph, seconds, @"display_including_anim_window_only")];
		[anim stop];
	} else {
		[streams addObject:@{@"label": @"display_including_anim_window_only", @"error": @"SCWindow for the anim window not found"}];
	}
	r[@"streams"] = streams;
	[anim.win close];
	return mpJSON(r);
}

#pragma mark - input（項目 2）

@interface MPInputView : NSView
@property (nonatomic) NSMutableArray *events;
@end
@implementation MPInputView
- (instancetype)initWithFrame:(NSRect)f {
	if ((self = [super initWithFrame:f])) _events = [NSMutableArray array];
	return self;
}
- (BOOL)acceptsFirstResponder { return YES; }
- (BOOL)acceptsFirstMouse:(NSEvent *)e { return YES; }
- (void)rec:(NSString *)t event:(NSEvent *)e {
	NSMutableDictionary *d = [NSMutableDictionary dictionaryWithObject:t forKey:@"type"];
	NSPoint p = [self convertPoint:e.locationInWindow fromView:nil];
	if (e.type == NSEventTypeLeftMouseDown || e.type == NSEventTypeLeftMouseUp || e.type == NSEventTypeRightMouseDown) {
		d[@"x"] = @(p.x);
		d[@"y"] = @(p.y);
		d[@"click_count"] = @(e.clickCount);
	} else if (e.type == NSEventTypeKeyDown || e.type == NSEventTypeKeyUp) {
		d[@"chars"] = e.characters ?: @"";
		d[@"key_code"] = @(e.keyCode);
	}
	[self.events addObject:d];
}
- (void)mouseDown:(NSEvent *)e { [self rec:@"mouseDown" event:e]; }
- (void)mouseUp:(NSEvent *)e { [self rec:@"mouseUp" event:e]; }
- (void)rightMouseDown:(NSEvent *)e { [self rec:@"rightMouseDown" event:e]; }
- (void)keyDown:(NSEvent *)e { [self rec:@"keyDown" event:e]; }
- (void)keyUp:(NSEvent *)e { [self rec:@"keyUp" event:e]; }
@end

typedef void (^PostFn)(CGEventRef);

static void postUnicode(CGEventSourceRef src, NSString *s, PostFn post) {
	UniChar buf[32];
	CFIndex n = MIN((CFIndex)s.length, (CFIndex)32);
	[s getCharacters:buf range:NSMakeRange(0, (NSUInteger)n)];
	for (int down = 1; down >= 0; down--) {
		CGEventRef e = CGEventCreateKeyboardEvent(src, 0, down);
		CGEventKeyboardSetUnicodeString(e, n, buf);
		post(e);
		CFRelease(e);
		usleep(20000);
	}
}

// 1 回分の送信。click・キー 'a'（keycode 0）・Unicode（日本語・絵文字）を post 経由で送る。
static NSDictionary *sendInputRound(MPInputView *view, NSWindow *win, NSString *label, PostFn post) {
	[view.events removeAllObjects];
	CGEventSourceRef src = CGEventSourceCreate(kCGEventSourceStatePrivate);
	NSRect wf = [win convertRectToScreen:[view convertRect:view.bounds toView:nil]];
	CGFloat primaryH = [NSScreen screens].firstObject.frame.size.height;
	CGPoint pt = CGPointMake(NSMidX(wf), primaryH - NSMidY(wf));
	CGEventRef mv = CGEventCreateMouseEvent(src, kCGEventMouseMoved, pt, kCGMouseButtonLeft);
	post(mv);
	CFRelease(mv);
	usleep(30000);
	for (int down = 1; down >= 0; down--) {
		CGEventRef e = CGEventCreateMouseEvent(src, down ? kCGEventLeftMouseDown : kCGEventLeftMouseUp, pt, kCGMouseButtonLeft);
		CGEventSetIntegerValueField(e, kCGMouseEventClickState, 1);
		post(e);
		CFRelease(e);
		usleep(30000);
	}
	for (int down = 1; down >= 0; down--) {
		CGEventRef e = CGEventCreateKeyboardEvent(src, 0 /* kVK_ANSI_A */, down);
		post(e);
		CFRelease(e);
		usleep(20000);
	}
	postUnicode(src, @"日本語", post);
	postUnicode(src, @"😀", post);
	CFRelease(src);
	pumpApp(0.6);
	NSString *typed = @"";
	BOOL clicked = NO;
	for (NSDictionary *d in view.events) {
		if ([d[@"type"] isEqual:@"keyDown"]) typed = [typed stringByAppendingString:d[@"chars"]];
		if ([d[@"type"] isEqual:@"mouseDown"]) clicked = YES;
	}
	return @{@"label": label, @"events": [view.events copy], @"typed": typed, @"click_received": @(clicked),
			 @"click_target_view_point": @{@"x": @(NSWidth(view.bounds) / 2), @"y": @(NSHeight(view.bounds) / 2)}};
}

static CGEventRef tapCb(CGEventTapProxy p, CGEventType t, CGEventRef e, void *u) { return e; }

char *mp_input(const char *outDirC, int allowGlobal) {
	initApp();
	NSMutableDictionary *r = [NSMutableDictionary dictionary];
	r[@"status"] = statusDict();
	BOOL locked = sessionLocked();
	NSWindow *win = makeWindow(NSMakeRect(300, 400, 500, 300), @"macprobe-input", NSWindowStyleMaskTitled);
	MPInputView *view = [[MPInputView alloc] initWithFrame:NSMakeRect(0, 0, 500, 300)];
	win.contentView = view;
	[win makeKeyAndOrderFront:nil];
	[NSApp activateIgnoringOtherApps:YES];
	[win makeFirstResponder:view];
	BOOL key = waitFor(^BOOL { return win.isKeyWindow; }, 2);
	r[@"window"] = @{@"visible": @(win.isVisible), @"key": @(key), @"app_active": @(NSApp.isActive),
					 @"occlusion_visible": @(MPB((win.occlusionState & NSWindowOcclusionStateVisible) != 0))};

	NSMutableArray *rounds = [NSMutableArray array];
	pid_t me = getpid();
	// 自プロセス宛て（HID タップを通さない。ロック中でも他アプリに届かない）。
	[rounds addObject:sendInputRound(view, win, @"post_to_self_pid", ^(CGEventRef e) { CGEventPostToPid(me, e); })];

	// グローバル（HID タップ）。ロック中・自窓がキー窓でない・明示指定なしのときは送らない。
	NSMutableDictionary *g = [NSMutableDictionary dictionary];
	if (!allowGlobal) {
		g[@"skipped"] = @"--allow-global-input not given";
	} else if (locked) {
		g[@"skipped"] = @"session is locked: refuse to post global input (could reach the lock screen)";
	} else if (!(win.isKeyWindow && NSApp.isActive)) {
		g[@"skipped"] = @"probe window is not the key window: refuse to post global input";
	} else {
		CGEventRef cur = CGEventCreate(NULL);
		CGPoint saved = CGEventGetLocation(cur);
		CFRelease(cur);
		NSDictionary *round = sendInputRound(view, win, @"post_hid_tap", ^(CGEventRef e) { CGEventPost(kCGHIDEventTap, e); });
		CGWarpMouseCursorPosition(saved);
		g = [round mutableCopy];
	}
	g[@"label"] = @"post_hid_tap";
	[rounds addObject:g];
	r[@"rounds"] = rounds;

	// 入力の「監視」（listen-only の tap）に入力監視の許可が要るか。送信（post）側の許可とは別。
	NSMutableDictionary *tap = [NSMutableDictionary dictionary];
	tap[@"input_monitoring_preflight"] = @(MPB(CGPreflightListenEventAccess()));
	CFMachPortRef mp = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap, kCGEventTapOptionListenOnly, CGEventMaskBit(kCGEventKeyDown), tapCb, NULL);
	tap[@"listen_only_tap_created"] = @(MPB(mp != NULL));
	if (mp) {
		CFMachPortInvalidate(mp);
		CFRelease(mp);
	}
	r[@"listen_tap"] = tap;
	[win close];
	return mpJSON(r);
}

#pragma mark - overlay（項目 3）

static NSString *runTask(NSString *path, NSArray *args, int *status) {
	NSTask *t = [[NSTask alloc] init];
	t.executableURL = [NSURL fileURLWithPath:path];
	t.arguments = args;
	NSPipe *p = [NSPipe pipe];
	t.standardOutput = p;
	t.standardError = p;
	NSError *e = nil;
	if (![t launchAndReturnError:&e]) {
		*status = -1;
		return errStr(e);
	}
	while (t.isRunning) pumpApp(0.05);
	*status = t.terminationStatus;
	NSData *d = [p.fileHandleForReading readDataToEndOfFile];
	return [[NSString alloc] initWithData:d encoding:NSUTF8StringEncoding] ?: @"";
}

char *mp_overlay(const char *outDirC) {
	initApp();
	NSString *outDir = [NSString stringWithUTF8String:outDirC];
	NSMutableDictionary *r = [NSMutableDictionary dictionary];
	r[@"status"] = statusDict();
	NSError *err = nil;
	SCShareableContent *c = fetchContent(&err);
	if (!c) {
		r[@"error"] = errStr(err) ?: @"nil content";
		return mpJSON(r);
	}
	SCDisplay *main = nil;
	for (SCDisplay *d in c.displays) if (d.displayID == CGMainDisplayID()) main = d;
	if (!main) main = c.displays.firstObject;
	SCContentFilter *probeFilter = [[SCContentFilter alloc] initWithDisplay:main excludingWindows:@[]];
	CGFloat scale = [SCShareableContent infoForFilter:probeFilter].pointPixelScale;
	size_t pw = (size_t)(main.width * scale), ph = (size_t)(main.height * scale);
	NSScreen *screen = [NSScreen screens].firstObject;
	NSRect sf = screen.frame;

	// 対照: 通常の窓（緑）。オーバーレイ: 画面全体の透明窓に、洋紅の札を 1 枚だけ描く（製品の枠と札の代用）。
	NSRect ctrlRect = NSMakeRect(sf.origin.x + 400, sf.origin.y + sf.size.height - 300 - 200, 300, 200);
	NSWindow *ctrl = makeWindow(ctrlRect, @"macprobe-control", NSWindowStyleMaskBorderless);
	ctrl.contentView = colorView(NSMakeRect(0, 0, 300, 200), [NSColor colorWithSRGBRed:0 green:1 blue:0 alpha:1]);
	ctrl.level = NSFloatingWindowLevel;
	ctrl.hasShadow = NO;
	ctrl.ignoresMouseEvents = YES;
	[ctrl orderFrontRegardless];

	NSWindow *ov = makeWindow(sf, @"macprobe-overlay", NSWindowStyleMaskBorderless);
	ov.opaque = NO;
	ov.backgroundColor = NSColor.clearColor;
	ov.hasShadow = NO;
	ov.level = NSScreenSaverWindowLevel;
	ov.ignoresMouseEvents = YES;
	ov.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorStationary | NSWindowCollectionBehaviorFullScreenAuxiliary;
	NSView *root = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, sf.size.width, sf.size.height)];
	root.wantsLayer = YES;
	NSRect markerLocal = NSMakeRect(50, sf.size.height - 150 - 120, 240, 120);
	[root addSubview:colorView(markerLocal, [NSColor colorWithSRGBRed:1 green:0 blue:1 alpha:1])];
	ov.contentView = root;
	[ov orderFrontRegardless];
	pumpApp(1.0);

	NSRect markerCocoa = NSMakeRect(sf.origin.x + markerLocal.origin.x, sf.origin.y + markerLocal.origin.y, markerLocal.size.width, markerLocal.size.height);
	CGRect markerPx = pxRect(markerCocoa, main.frame, scale);
	CGRect ctrlPx = pxRect(ctrlRect, main.frame, scale);
	r[@"marker_px"] = rectDict(markerPx);
	r[@"control_px"] = rectDict(ctrlPx);
	r[@"scale"] = @(scale);
	r[@"overlay_window_number"] = @(ov.windowNumber);
	r[@"overlay_on_screen"] = @(ov.isVisible);

	SCWindow *scOv = nil, *scCtrl = nil;
	for (int i = 0; i < 20 && !(scOv && scCtrl); i++) {
		NSError *e = nil;
		SCShareableContent *c2 = fetchContent(&e);
		for (SCWindow *w in c2.windows) {
			if (w.windowID == (CGWindowID)ov.windowNumber) scOv = w;
			if (w.windowID == (CGWindowID)ctrl.windowNumber) scCtrl = w;
		}
		if (!(scOv && scCtrl)) pumpApp(0.2);
	}
	r[@"overlay_in_scshareablecontent"] = @(scOv != nil);

	NSMutableArray *variants = [NSMutableArray array];
	void (^shot)(NSString *, NSString *, SCContentFilter *) = ^(NSString *name, NSString *desc, SCContentFilter *f) {
		NSMutableDictionary *v = [NSMutableDictionary dictionaryWithDictionary:@{@"name": name, @"desc": desc, @"sharing_type": @(ov.sharingType)}];
		NSError *e = nil;
		CGImageRef img = f ? captureShot(f, pw, ph, &e) : NULL;
		if (img) {
			NSString *p = [outDir stringByAppendingPathComponent:[name stringByAppendingString:@".png"]];
			v[@"file"] = p.lastPathComponent;
			v[@"saved"] = @(savePNG(img, p));
			CGImageRelease(img);
		} else {
			v[@"error"] = errStr(e) ?: @"nil image";
		}
		[variants addObject:v];
	};
	void (^cli)(NSString *, NSString *) = ^(NSString *name, NSString *desc) {
		NSMutableDictionary *v = [NSMutableDictionary dictionaryWithDictionary:@{@"name": name, @"desc": desc, @"sharing_type": @(ov.sharingType)}];
		NSString *p = [outDir stringByAppendingPathComponent:[name stringByAppendingString:@".png"]];
		int st = 0;
		NSString *out = runTask(@"/usr/sbin/screencapture", @[@"-x", @"-t", @"png", p], &st);
		v[@"exit_status"] = @(st);
		v[@"tool_output"] = [out stringByTrimmingCharactersInSet:NSCharacterSet.whitespaceAndNewlineCharacterSet];
		if ([[NSFileManager defaultManager] fileExistsAtPath:p]) v[@"file"] = p.lastPathComponent;
		else v[@"error"] = @"screencapture produced no file";
		[variants addObject:v];
	};

	NSArray *incl = (scOv && scCtrl) ? @[scCtrl, scOv] : nil;
	ov.sharingType = NSWindowSharingReadOnly;
	pumpApp(0.3);
	shot(@"v1_display_all", @"ディスプレイ全体（除外なし）。sharingType=ReadOnly", probeFilter);
	shot(@"v4_include_control_and_overlay", @"対照窓とオーバーレイだけを合成。sharingType=ReadOnly", incl ? [[SCContentFilter alloc] initWithDisplay:main includingWindows:incl] : nil);
	if (scOv) shot(@"v3_display_exclude_overlay", @"ディスプレイ全体からオーバーレイ窓を SCContentFilter で除外。sharingType=ReadOnly", [[SCContentFilter alloc] initWithDisplay:main excludingWindows:@[scOv]]);
	cli(@"v6_screencapture_cli", @"/usr/sbin/screencapture -x（OS 標準のスクリーンショット）。sharingType=ReadOnly");

	ov.sharingType = NSWindowSharingNone;
	pumpApp(0.5);
	shot(@"v2_display_all_sharing_none", @"ディスプレイ全体（除外なし）。sharingType=None", probeFilter);
	shot(@"v5_include_control_and_overlay_sharing_none", @"対照窓とオーバーレイだけを合成。sharingType=None", incl ? [[SCContentFilter alloc] initWithDisplay:main includingWindows:incl] : nil);
	cli(@"v7_screencapture_cli_sharing_none", @"/usr/sbin/screencapture -x。sharingType=None");

	r[@"variants"] = variants;
	[ov close];
	[ctrl close];
	return mpJSON(r);
}
