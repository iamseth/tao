package gitops

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestDeltaListsTruncation(t *testing.T) {
	for _, tc := range []struct {
		kind, raw string
		ends      []int
	}{
		{"name", "A\x00a\x00R100\x00old\x00new\x00", []int{4, 17}},
		{"num", "1\t2\ta\x003\t4\t\x00old\x00new\x00", []int{6, 19}},
		{"status", "? a\x002 R. N... 100644 100644 100644 abc def R100 new\x00old\x00", nil},
	} {
		if tc.ends == nil {
			tc.ends = []int{4, len(tc.raw)}
		}
		tc.ends[len(tc.ends)-1] = len(tc.raw)
		for cap := 1; cap <= len(tc.raw)+1; cap++ {
			c := NewClient(t.TempDir(), func(_ context.Context, _, _ string, _ []string, out, _ io.Writer) error {
				for _, b := range []byte(tc.raw) {
					if n, err := out.Write([]byte{b}); n != 1 || err != nil {
						t.Fatal("not drained")
					}
				}
				return nil
			})
			count := 0
			for _, end := range tc.ends {
				if end <= cap {
					count++
				}
			}
			var n int
			var trunc bool
			var err error
			switch tc.kind {
			case "name":
				var got []NameStatusEntry
				got, trunc, err = c.DiffNameStatusZ(context.Background(), cap)
				n = len(got)
			case "num":
				var got []NumstatEntry
				got, trunc, err = c.DiffNumstatZ(context.Background(), cap)
				n = len(got)
			default:
				var got string
				got, trunc, err = c.StatusPorcelainV2Z(context.Background(), cap)
				wantEnd := 0
				for _, end := range tc.ends {
					if end <= cap {
						wantEnd = end
					}
				}
				if got != tc.raw[:wantEnd] {
					t.Fatalf("status cap %d: %q", cap, got)
				}
				n = count
			}
			if err != nil || n != count || trunc != (cap < len(tc.raw)) {
				t.Fatalf("%s cap %d: n=%d want=%d truncated=%v err=%v", tc.kind, cap, n, count, trunc, err)
			}
		}
	}
}

func TestParseNameStatusZ(t *testing.T) {
	raw := "A\x00 space\n雪\x00M\x00m\x00D\x00d\x00T\x00t\x00R100\x00old\x00new\x00C75\x00src\x00dst\x00"
	got, err := ParseNameStatusZ(raw)
	if err != nil || len(got) != 6 || got[0].Path != " space\n雪" || !reflect.DeepEqual(got[4], NameStatusEntry{Status: "R100", OldPath: "old", Path: "new"}) {
		t.Fatalf("%+v %v", got, err)
	}
	for _, raw := range []string{"A", "A\x00", "A\x00\x00", "Q\x00x\x00", "R101\x00a\x00b\x00", "R\x00a\x00b\x00", "M\x00x\x00\x00"} {
		if _, err := ParseNameStatusZ(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func FuzzDeltaLists(f *testing.F) {
	for _, s := range []string{"", "A\x00a\x00", "R100\x00old\x00new\x00", "-\t-\tb\x00", "1\t2\t\x00old\x00new\x00", "? a\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		_, _ = ParseNameStatusZ(raw)
		for _, kind := range []string{"name", "num", "status"} {
			for _, trunc := range []bool{false, true} {
				complete, err := completeList(raw, trunc, kind)
				if err == nil && !strings.HasPrefix(raw, complete) {
					t.Fatal("not a prefix")
				}
			}
		}
	})
}

func TestDeltaListCommands(t *testing.T) {
	for _, kind := range []string{"name", "num", "status"} {
		root := t.TempDir()
		payload := "A\x00p\x00"
		command := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-status", "-z", "HEAD", "--", " literal\n雪"}
		if kind == "num" {
			payload = "-\t-\tb\x001\t2\t\x00old\x00 new\n雪\x00"
			command[5] = "--numstat"
		}
		if kind == "status" {
			payload = "? p\x00"
			command = []string{"status", "--porcelain=v2", "-z", "--untracked-files=all", "--no-renames"}
		}
		calls := 0
		c := NewClient(root, func(ctx context.Context, cwd, name string, args []string, out, stderr io.Writer) error {
			calls++
			want := append([]string{"-C", root, "--no-optional-locks", "-c", "diff.autoRefreshIndex=false", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "--literal-pathspecs"}, command...)
			if cwd != "" || name != "git" || !reflect.DeepEqual(args, want) {
				t.Fatalf("argv %q", args)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			_, _ = io.WriteString(out, payload)
			return nil
		})
		invoke := func(ctx context.Context, limit int) error {
			switch kind {
			case "name":
				_, _, err := c.DiffNameStatusZ(ctx, limit, "HEAD", "--", " literal\n雪")
				return err
			case "num":
				got, _, err := c.DiffNumstatZ(ctx, limit, "HEAD", "--", " literal\n雪")
				if err == nil && (len(got) != 2 || !got[0].Binary || got[1].OldPath != "old" || got[1].Path != " new\n雪" || got[1].Added != 1 || got[1].Deleted != 2) {
					t.Fatalf("numstat %+v", got)
				}
				return err
			default:
				_, _, err := c.StatusPorcelainV2Z(ctx, limit)
				return err
			}
		}
		for _, limit := range []int{0, -1} {
			if invoke(context.Background(), limit) == nil {
				t.Fatal("invalid limit")
			}
		}
		if calls != 0 {
			t.Fatal("invalid limit invoked runner")
		}
		if err := invoke(context.Background(), 1000); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := invoke(ctx, 1000); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	}
}

func TestDeltaListStderrBound(t *testing.T) {
	c := NewClient(t.TempDir(), func(_ context.Context, _, _ string, _ []string, _, stderr io.Writer) error {
		payload := strings.Repeat("diagnostic", probeOutputLimit)
		if n, err := io.WriteString(stderr, payload); n != len(payload) || err != nil {
			t.Fatal("stderr not drained")
		}
		return probeExit(2)
	})
	for _, kind := range []string{"name", "num", "status"} {
		_, _, err := c.deltaList(context.Background(), 10, kind, []string{"status"})
		if err == nil || len(err.Error()) > probeOutputLimit+1024 || !strings.Contains(err.Error(), "stderr truncated") {
			t.Fatalf("diagnostic: %v", err)
		}
	}
}

func TestCompleteListEveryByte(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		records []string
	}{
		{"name", []string{"A\x00 space\n雪\x00", "M\x00m\x00", "D\x00d\x00", "T\x00t\x00", "C75\x00old\n雪\x00new\x00", "R100\x00a\x00b\x00"}},
		{"num", []string{"1\t0\t space\n雪\x00", "-\t-\tbinary\x00", "0\t12\t\x00old\x00new\n雪\x00", "-\t-\t\x00source\x00dest\x00"}},
		{"status", []string{"? space\n雪\x00", "! ignored\x00", "1 .M N... 100644 100644 100644 abc def changed\x00", "2 R. N... 100644 100644 100644 abc def R100 new\x00old\n雪\x00", "u UU N... 100644 100644 100644 100644 abc def abc conflict\x00"}},
	} {
		raw := strings.Join(tc.records, "")
		for cap := 0; cap <= len(raw); cap++ {
			end := 0
			for _, record := range tc.records {
				if end+len(record) > cap {
					break
				}
				end += len(record)
			}
			got, err := completeList(raw[:cap], cap < len(raw), tc.kind)
			if err != nil || got != raw[:end] {
				t.Fatalf("%s cap %d: %q %v", tc.kind, cap, got, err)
			}
			if cap > 0 && cap != end {
				if _, err := completeList(raw[:cap], false, tc.kind); err == nil {
					t.Fatalf("accepted incomplete %s cap %d", tc.kind, cap)
				}
			}
		}
	}
}

func TestDeltaListMalformed(t *testing.T) {
	for _, tc := range []struct{ kind, raw string }{
		{"name", "A\x00\x00"}, {"name", "R200\x00a\x00b\x00"},
		{"num", "-\t1\tp\x00"}, {"num", "-1\t2\tp\x00"}, {"num", "9999999999999999999999\t2\tp\x00"}, {"num", "1\t2\t\x00\x00p\x00"},
		{"status", "? \x00"}, {"status", "z p\x00"}, {"status", "1 bad\x00"}, {"status", "2 R. N... 100644 100644 100644 a b R100 new\x00\x00"},
	} {
		for _, trunc := range []bool{false, true} {
			if _, err := completeList(tc.raw, trunc, tc.kind); err == nil {
				t.Fatalf("accepted %s %q trunc=%v", tc.kind, tc.raw, trunc)
			}
		}
	}
}
