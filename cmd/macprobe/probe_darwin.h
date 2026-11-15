// cgo から呼ぶ Objective-C 側の入口。戻り値は JSON（mp_free で解放する）。
#ifndef MACPROBE_PROBE_DARWIN_H
#define MACPROBE_PROBE_DARWIN_H

char *mp_status(void);
// 画面収録・アクセシビリティの許可を OS に要求する（システムの確認ダイアログが出る。許可そのものは回避しない）。
char *mp_request(void);
char *mp_windows(int fixturePID, const char *fixtureTitle);
char *mp_capture(const char *outDir, double seconds);
char *mp_input(const char *outDir, int allowGlobal);
char *mp_overlay(const char *outDir);
// テスト窓を 1 枚出して seconds 秒（または標準入力の EOF まで）待つ。窓を出したら標準出力に "READY <pid> <windowNumber>" を書く。
int mp_fixture(const char *title, double seconds);
void mp_free(char *p);

#endif
