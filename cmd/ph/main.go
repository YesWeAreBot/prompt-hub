package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yeswearebot/prompt-hub/internal/httpapi"
	"github.com/yeswearebot/prompt-hub/internal/hub"
	"github.com/yeswearebot/prompt-hub/internal/ledger"
	"github.com/yeswearebot/prompt-hub/internal/pack"
	"github.com/yeswearebot/prompt-hub/internal/phpfetch"
	"github.com/yeswearebot/prompt-hub/internal/profile"
	"github.com/yeswearebot/prompt-hub/internal/render"
	"github.com/yeswearebot/prompt-hub/internal/store"
)

const defaultAddr = "127.0.0.1:18400"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "serve":
		err = cmdServe(args)
	case "install":
		err = cmdInstall(args)
	case "check":
		err = cmdCheck(args)
	case "test":
		err = cmdTest(args)
	case "list":
		err = cmdList(args)
	case "render":
		err = cmdRender(args)
	case "hash":
		err = cmdHash(args)
	case "ledger":
		err = cmdLedger(args)
	case "hub":
		err = cmdHub(args)
	case "publish":
		err = cmdPublish(args)
	case "index":
		err = cmdIndex(args)
	case "upgrade":
		err = cmdUpgrade(args)
	case "runtime":
		err = cmdRuntime(args)
	case "remove":
		err = cmdRemove(args)
	case "gc":
		err = cmdGC(args)
	case "export":
		err = cmdExport(args)
	case "profile":
		err = cmdProfile(args)
	case "lock":
		err = cmdLock(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		err = fmt.Errorf("未知子命令 %q", cmd)
	}
	if err == nil {
		return
	}
	var missing *render.MissingContextError
	var conflict *store.VersionConflictError
	switch {
	case errors.As(err, &missing):
		fmt.Fprintf(os.Stderr, "契约违例: %v\n", err)
	case errors.As(err, &conflict):

		fmt.Fprintf(os.Stderr, "%v\n", err)
	default:
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
	}
	os.Exit(1)
}

func usage() {
	fmt.Fprint(os.Stderr, `ph — prompt pack 的本机 daemon 与 CLI

  ph serve   [--addr 127.0.0.1:18400] [--store DIR] [--token-file FILE]
             [--php PATH] [--views DIR]
             [--render-timeout 5s] [--max-render-memory-mb 256] [--max-render-procs 8]
             [--allow-egress host1,host2]
  ph install <pack 路径>            [--store DIR]
  ph check   <pack 路径>
  ph test    <pack 路径>
  ph list                           [--store DIR]
  ph render  <id@version> [--ctx k=v]... [--turn ID] [--etag E] [--addr HOST]
  ph hash    <pack 路径>
  ph ledger  [--n 20] [--ledger FILE]

  ph hub serve [--addr 127.0.0.1:18500] [--root DIR] [--publish-token T]
  ph publish <pack 路径> --hub URL [--token T]
  ph index   --hub URL
  ph upgrade <packId> --hub URL [--yes]     # 先看能力差异，再决定升不升

  ph runtime                                # 列出运行时档位与启用方式
  ph runtime enable php                     # 按需下载便携 PHP（校验官方 sha256）

  ph remove  <id@version>                   # 从 store 删掉一个版本
  ph gc                                     # 清理不完整的安装残留
  ph export  <id@version> --to DIR          # 导出成内嵌层布局（配合 serve --embed）

  ph profile [name/version]                 # 打印字段元数据表（稳定性 / 隐私）
  ph lock    <pack 路径> [--update]         # 把 includes 里的范围钉成 pack.lock
`)
}

func homeDir() string {
	if v := os.Getenv("PH_HOME"); v != "" {
		return v
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(h, ".prompt-hub")
}

func defaultStore() string  { return filepath.Join(homeDir(), "store") }
func defaultToken() string  { return filepath.Join(homeDir(), "token") }
func defaultLedger() string { return filepath.Join(homeDir(), "ledger.jsonl") }
func defaultViews() string  { return filepath.Join(homeDir(), "views") }

type runtimeTier struct {
	Name      string
	Available bool
	Detail    string
	Enable    string
}

func tierStates(home, phpPath string) []runtimeTier {
	tiers := []runtimeTier{
		{Name: "template", Available: true, Detail: "内置（纯文本 + {{field}}）"},
		{Name: "wasm", Available: true, Detail: "内置（wazero + WASI 沙箱）"},
	}
	bin, reason := render.FindPHP(phpPath, home)
	if bin != "" {
		tiers = append(tiers, runtimeTier{Name: "php", Available: true, Detail: "php (" + bin + ")"})
	} else {
		tiers = append(tiers, runtimeTier{
			Name: "php", Detail: reason,
			Enable: "ph runtime enable php（下载便携 PHP 到 $PH_HOME/tools/php）",
		})
	}
	return tiers
}

func tierSummary(tiers []runtimeTier) string {
	var parts []string
	for _, t := range tiers {
		if t.Available {
			parts = append(parts, t.Name)
		} else {
			parts = append(parts, t.Name+"(未启用)")
		}
	}
	return strings.Join(parts, ", ")
}

func newRegistry(home, phpPath, views string, limits render.Limits, egress []string) (*render.Registry, []runtimeTier) {
	tiers := tierStates(home, phpPath)
	r := render.NewRegistry()
	for _, t := range tiers {
		r.SetHint(t.Name, t.Enable)
	}
	r.Register(render.TemplateRuntime{})

	r.Register(render.NewWasmRuntime(limits).WithEgress(egress).WithViewRoot(views))
	if bin, _ := render.FindPHP(phpPath, home); bin != "" {
		r.Register(render.NewPHPRuntime(bin, views, limits).WithEgress(egress))
	}
	return r, tiers
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func reorderFlags(args []string, valueFlags map[string]bool) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.ContainsRune(name, '=') {
			continue
		}
		if valueFlags[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, pos...)
}

func prewarmWasm(reg *render.Registry, resolver *store.Resolver) (int, int64) {
	rt, err := reg.Get("wasm")
	if err != nil {
		return 0, 0
	}
	wrt, ok := rt.(*render.WasmRuntime)
	if !ok {
		return 0, 0
	}
	all, err := resolver.All()
	if err != nil {
		return 0, 0
	}
	start := time.Now()
	n := 0
	for _, ref := range all {
		id, ver, ok := strings.Cut(ref, "@")
		if !ok {
			continue
		}
		resolved, err := resolver.Resolve(id, ver)
		if err != nil {
			continue
		}
		loaded, err := pack.Load(resolved.Dir)
		if err != nil || loaded.Manifest.Runtime != "wasm" {
			continue
		}
		if err := wrt.Prewarm(resolved.Dir, loaded.Manifest.Entry, resolved.TreeHash); err == nil {
			n++
		}
	}
	return n, time.Since(start).Milliseconds()
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", defaultAddr, "监听地址（只应绑 127.0.0.1）")
	storeDir := fs.String("store", defaultStore(), "store 目录")
	tokenFile := fs.String("token-file", defaultToken(), "token 文件")
	ledgerFile := fs.String("ledger", defaultLedger(), "ledger 文件")
	noToken := fs.Bool("no-token", false, "跳过 token 校验（仅本地测试）")
	phpPath := fs.String("php", "", "php 可执行文件路径（默认自动查找）")
	views := fs.String("views", defaultViews(), "渲染视图临时目录")
	maxMem := fs.Int("max-render-memory-mb", 256, "单次渲染的内存上限 MB（0=不限）")
	maxProcs := fs.Int("max-render-procs", 8, "单次渲染的进程数上限（0=不限）")
	timeout := fs.Duration("render-timeout", 5*time.Second, "单次渲染的超时")
	egressFlag := fs.String("allow-egress", "", "允许 pack 出网的 host，逗号分隔（默认全禁）")
	embedFlag := fs.String("embed", "", "内嵌只读默认 pack 目录，逗号分隔（按 <packId>@<version>/ 布局）")
	ctxEndpoint := fs.String("context-endpoint", "", "按需取 context 的回调地址（必须是回环地址）")
	ctxToken := fs.String("context-token", "", "回调宿主时带的 Bearer token")
	profilesDir := fs.String("profiles", defaultProfilesDir(), "额外的 profile 目录（<name>/<version>.yaml）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !strings.HasPrefix(*addr, "127.0.0.1") && !*noToken {
		fmt.Fprintln(os.Stderr, "警告: 正在绑定非 127.0.0.1 地址，本机 daemon 不应对外暴露")
	}

	token, err := loadOrCreateToken(*tokenFile)
	if err != nil {
		return err
	}
	lg, err := ledger.Open(*ledgerFile)
	if err != nil {
		return err
	}

	limits := render.Limits{
		WallTimeout:  *timeout,
		MemoryBytes:  int64(*maxMem) << 20,
		MaxProcesses: *maxProcs,
	}
	egress := splitCSV(*egressFlag)
	embedDirs := splitCSV(*embedFlag)
	registry, tiers := newRegistry(homeDir(), *phpPath, *views, limits, egress)

	resolver := &store.Resolver{Store: store.New(*storeDir), Embed: embedDirs}

	var tierDescs []httpapi.TierDesc
	for _, t := range tiers {
		tierDescs = append(tierDescs, httpapi.TierDesc{
			Name: t.Name, Available: t.Available, Detail: t.Detail, Hint: t.Enable,
		})
	}

	profiles, err := profile.NewRegistry(*profilesDir)
	if err != nil {
		return fmt.Errorf("装载 profile 失败: %w", err)
	}

	srv := httpapi.New(httpapi.Config{
		Resolver:        resolver,
		Runtimes:        registry,
		Ledger:          lg,
		Token:           token,
		AllowNoToken:    *noToken,
		AllowedEgress:   egress,
		Tiers:           tierDescs,
		Profiles:        profiles,
		ContextEndpoint: *ctxEndpoint,
		ContextToken:    *ctxToken,
		ContextTimeout:  2 * time.Second,
	})

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}

	fmt.Printf("prompt-hub daemon  listening on http://%s\n", ln.Addr())
	fmt.Printf("  store   %s\n", *storeDir)
	fmt.Printf("  ledger  %s\n", *ledgerFile)
	fmt.Printf("  token   %s\n", *tokenFile)
	fmt.Printf("  views   %s\n", *views)
	if *noToken {
		fmt.Println("  token   (已禁用)")
	}
	fmt.Printf("  档位    %s\n", tierSummary(tiers))
	fmt.Printf("  沙箱    timeout=%s  memory=%dMB  procs=%d\n", limits.WallTimeout, *maxMem, *maxProcs)
	if len(egress) == 0 {
		fmt.Println("  出网    全禁（pack 声明 egress 会被拒）")
	} else {
		fmt.Printf("  出网    %s\n", strings.Join(egress, ", "))
	}
	if len(embedDirs) > 0 {
		fmt.Printf("  内嵌层  %s（store 优先，用户可覆盖应用自带默认）\n", strings.Join(embedDirs, ", "))
	}
	all, _ := resolver.All()
	fmt.Printf("  已装 pack: %d 个\n", len(all))
	if n, ms := prewarmWasm(registry, resolver); n > 0 {
		fmt.Printf("  wasm 预热: %d 个模块，%dms（期间请求排队，不会被拒）\n", n, ms)
	}
	return http.Serve(ln, srv.Handler())
}

func loadOrCreateToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tok := randomToken()
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

func randomToken() string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 32)
	seed := time.Now().UnixNano()
	for i := range b {
		seed = seed*6364136223846793005 + 1442695040888963407
		b[i] = chars[uint64(seed)>>33%uint64(len(chars))]
	}
	return string(b)
}

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	storeDir := fs.String("store", defaultStore(), "store 目录")
	hubURL := fs.String("hub", "", "从 hub 安装（参数写成 id@version）")
	noDeps := fs.Bool("no-deps", false, "不自动安装依赖")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"store": true, "hub": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("用法: ph install <pack 路径> | ph install <id@version> --hub URL")
	}
	target := fs.Arg(0)

	if *hubURL != "" {
		ref, err := pack.ParseRef(target)
		if err != nil {
			return fmt.Errorf("从 hub 安装需要 id@version: %w", err)
		}
		return installFromHub(store.New(*storeDir), *hubURL, ref, !*noDeps)
	}

	path := target
	loaded, err := pack.Load(path)
	if err != nil {
		return err
	}
	if err := loaded.Manifest.Validate(); err != nil {
		return fmt.Errorf("manifest 不合法: %w", err)
	}
	for _, w := range loaded.Warnings {
		fmt.Fprintf(os.Stderr, "警告: %s\n", w)
	}

	st := store.New(*storeDir)
	lock, err := pack.LockFromTree(loaded.Tree)
	if err != nil {
		return err
	}
	refs, err := loaded.Manifest.ExactRefs(lock)
	if err != nil {
		return fmt.Errorf("includes: %w", err)
	}
	var missingDeps []string
	for _, ref := range refs {
		if _, _, err := st.Resolve(ref.ID, ref.Version); err != nil {
			missingDeps = append(missingDeps, ref.String())
		}
	}
	if len(missingDeps) > 0 {
		return fmt.Errorf("缺少依赖，请先安装: %s", strings.Join(missingDeps, ", "))
	}

	dir, err := st.Install(loaded)
	if err != nil {
		return err
	}
	fmt.Printf("已安装 %s@%s\n  tree hash  %s\n  目录       %s\n",
		loaded.Manifest.PackID, loaded.Manifest.Version, loaded.Tree.Hash(), dir)
	if loaded.SingleFile {
		fmt.Println("  形态       单文件（manifest 已从头部注释规范化为 pack.yaml）")
	}

	reg, _ := newRegistry(homeDir(), "", defaultViews(), render.Limits{}, nil)
	if _, err := reg.Get(loaded.Manifest.Runtime); err != nil {
		fmt.Printf("  注意       %v\n", err)
	}
	return nil
}

func cmdCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	profilesDir := fs.String("profiles", defaultProfilesDir(), "额外的 profile 目录")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"profiles": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("用法: ph check <pack 路径> [--profiles DIR]")
	}
	loaded, err := pack.Load(fs.Arg(0))
	if err != nil {
		return err
	}
	canon, _ := loaded.Manifest.Canonical()
	fmt.Print(string(canon))
	fmt.Printf("---\ntree hash: %s\n", loaded.Tree.Hash())
	fmt.Printf("文件数:    %d\n", len(loaded.Tree))
	for _, w := range loaded.Warnings {
		fmt.Printf("警告:      %s\n", w)
	}
	if err := loaded.Manifest.Validate(); err != nil {
		return fmt.Errorf("manifest 不合法: %w", err)
	}
	fmt.Println("manifest:  合法")

	lock, err := pack.LockFromTree(loaded.Tree)
	if err != nil {
		return err
	}
	for _, inc := range loaded.Manifest.Includes {
		c, err := pack.ParseInclude(inc)
		if err != nil {
			return err
		}
		if !c.IsRange() {
			continue
		}
		e, ok := lock.Get(c.ID)
		if !ok {
			return fmt.Errorf("依赖 %s 是范围，但 %s 里没有它的条目：运行 ph lock <pack 路径>",
				inc, pack.LockName)
		}
		ref, err := pack.ParseRef(e.Ref)
		if err != nil {
			return err
		}
		if !c.Satisfies(ref.Version) {
			return fmt.Errorf("%s 里钉的是 %s，不满足 %s 的范围：运行 ph lock --update",
				pack.LockName, e.Ref, inc)
		}
		fmt.Printf("依赖锁定:  %s → %s\n", inc, e.Ref)
	}

	m := loaded.Manifest
	if m.Kind == "library" {
		fmt.Println("字段画像:  跳过（library pack 自己不出文本）")
		return nil
	}
	reg, err := profile.NewRegistry(*profilesDir)
	if err != nil {
		return err
	}
	p, ok := reg.Get(m.Profile)
	if !ok {
		return fmt.Errorf("profile %s 本机没有；可用：%s（用 --profiles DIR 提供它）",
			m.Profile, strings.Join(reg.Names(), ", "))
	}
	rep := p.Classify(m.Requires)
	if len(rep.Unknown) > 0 {

		return fmt.Errorf("%s 里没有这些字段：%s\n  是拼错了，还是该把它们加到 profile 里？",
			m.Profile, strings.Join(rep.Unknown, ", "))
	}
	fmt.Printf("字段画像:  %s，最高隐私 %s\n", rep.Placement, rep.MaxPrivacy)
	for _, f := range rep.Fields {
		fmt.Printf("  %-28s %-12s %s\n", f.Name, f.Stability, f.Privacy)
	}
	switch rep.Placement {
	case profile.PlacementFrozenGlobal:
		fmt.Println("  → 只依赖 static 字段，可进全局冻结前缀")
	case profile.PlacementFrozenSession:
		fmt.Println("  → 依赖 session 字段，只能按会话冻结（换会话 / 换群要重算）")
	default:
		fmt.Println("  → 依赖 per-request 字段，必须进每轮追加的尾段")
	}
	if rep.HasPII {
		fmt.Println("  ⚠ 含 pii：发给远端模型前应当让用户可见")
	}

	if m.Runtime == "template" {
		entryText := string(loaded.Tree[m.Entry])
		var deps map[string]pack.Tree
		resolver := &store.Resolver{Store: store.New(defaultStore())}
		if d, derr := resolver.ResolveClosure(loaded); derr == nil {
			deps = d
		} else {
			fmt.Printf("资产校验:  只查声明（依赖未解析：%v）\n", derr)
		}
		if err := render.CheckAssets(entryText, loaded.Tree, m.Includes, deps); err != nil {
			return fmt.Errorf("资产引用有问题: %w", err)
		}

		if deps != nil {
			bad, err := render.UndeclaredAssetVars(entryText, loaded.Tree, m.Requires, deps)
			if err != nil {
				return fmt.Errorf("资产闭包校验失败: %w", err)
			}
			if len(bad) > 0 {
				return fmt.Errorf("资产（含递归展开的那些）用了这些字段，但 requires 里没声明：%s\n"+
					"  补进 requires，宿主才会提供它们", strings.Join(bad, ", "))
			}
		}
		if n := len(render.AssetRefs(entryText)); n > 0 {

			if deps == nil {
				fmt.Printf("资产引用:  %d 个，只校验了 includes 声明（依赖未装，文件是否存在**没验证**）\n", n)
			} else {
				fmt.Printf("资产引用:  %d 个，全部通过（含文件存在性）\n", n)
			}
		}
	}
	return nil
}

func cmdHash(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("用法: ph hash <pack 路径>")
	}
	loaded, err := pack.Load(args[0])
	if err != nil {
		return err
	}
	fmt.Println(loaded.Tree.Hash())
	return nil
}

type fixture struct {
	Name    string            `json:"name"`
	Turn    string            `json:"turn"`
	Now     string            `json:"now"`
	Context map[string]string `json:"context"`
	Expect  string            `json:"expect"`
}

func cmdTest(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("用法: ph test <pack 路径>")
	}
	packPath := args[0]
	loaded, err := pack.Load(packPath)
	if err != nil {
		return err
	}
	if err := loaded.Manifest.Validate(); err != nil {
		return fmt.Errorf("manifest 不合法: %w", err)
	}
	fixtureDir := filepath.Join(packPath, "fixtures")
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {

		if loaded.Archived {
			return fmt.Errorf("归档里没有 fixtures；fixtures 只存在于源目录 —— 请对源目录跑 ph test")
		}
		return fmt.Errorf("没有 fixtures 目录: %w", err)
	}
	registry, _ := newRegistry(homeDir(), "", defaultViews(),
		render.Limits{WallTimeout: 10 * time.Second}, nil)
	rt, err := registry.Get(loaded.Manifest.Runtime)
	if err != nil {

		fmt.Printf("跳过 %s@%s：%v\n", loaded.Manifest.PackID, loaded.Manifest.Version, err)
		return nil
	}

	resolver := &store.Resolver{Store: store.New(defaultStore())}
	deps, err := resolver.ResolveClosure(loaded)
	if err != nil {
		return fmt.Errorf("依赖解析失败: %w", err)
	}

	dir := packPath
	fail := 0
	total := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(fixtureDir, e.Name()))
		if err != nil {
			return err
		}
		var fx fixture
		if err := json.Unmarshal(raw, &fx); err != nil {
			return fmt.Errorf("fixtures/%s 解析失败: %w", e.Name(), err)
		}
		now := time.Unix(0, 0)
		if fx.Now != "" {
			if parsed, err := time.Parse(time.RFC3339, fx.Now); err == nil {
				now = parsed
			}
		}
		total++
		res, err := rt.Render(render.Request{
			Dir: dir, Manifest: loaded.Manifest, TreeHash: loaded.Tree.Hash(), Tree: loaded.Tree,
			Deps: deps, Turn: fx.Turn, Now: now, Context: fx.Context,
		})
		name := fx.Name
		if name == "" {
			name = e.Name()
		}
		if err != nil {
			fail++
			fmt.Printf("FAIL  %s\n      %v\n", name, err)
			continue
		}
		if fx.Expect == "" {
			fmt.Printf("RENDER %s\n%s\n", name, indent(res.Body))
			continue
		}
		if strings.TrimRight(res.Body, "\n") == strings.TrimRight(fx.Expect, "\n") {
			fmt.Printf("PASS  %s\n", name)
			continue
		}
		fail++
		fmt.Printf("FAIL  %s\n--- 实际 ---\n%s\n--- 期望 ---\n%s\n", name, indent(res.Body), indent(fx.Expect))
	}
	fmt.Printf("\n%d/%d 通过\n", total-fail, total)
	if fail > 0 {
		return fmt.Errorf("%d 个 fixture 失败", fail)
	}
	return nil
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "      " + l
	}
	return strings.Join(lines, "\n")
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	storeDir := fs.String("store", defaultStore(), "store 目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	all, err := store.New(*storeDir).All()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Println("（store 为空）")
		return nil
	}
	for _, p := range all {
		fmt.Println(p)
	}
	return nil
}

func cmdLedger(args []string) error {
	fs := flag.NewFlagSet("ledger", flag.ExitOnError)
	n := fs.Int("n", 20, "最近 n 条")
	ledgerFile := fs.String("ledger", defaultLedger(), "ledger 文件")
	if err := fs.Parse(args); err != nil {
		return err
	}
	lg, err := ledger.Open(*ledgerFile)
	if err != nil {
		return err
	}
	entries, err := lg.Tail(*n)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("（ledger 为空）")
		return nil
	}
	for _, e := range entries {
		flags := ""
		if e.Source != "" {
			flags += " [" + e.Source + "]"
		}

		if e.Placement != "" {
			flags += " " + e.Placement
		}
		if e.MaxPrivacy == "pii" {
			flags += " ⚠pii"
		} else if e.MaxPrivacy != "" {
			flags += " " + e.MaxPrivacy
		}
		if e.Degraded {
			flags += " DEGRADED"
		}
		if e.NotMod {
			flags += " 304"
		}
		fmt.Printf("%s  %s@%s  %s  %dms%s\n",
			e.Time.Format("15:04:05"), e.Pack, e.Version, e.Status, e.Ms, flags)
		if e.Error != "" {
			fmt.Printf("            %s\n", e.Error)
		}
	}
	return nil
}

type ctxFlag map[string]string

func (c ctxFlag) String() string { return "" }
func (c ctxFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("--ctx 需要 k=v 形式")
	}
	c[strings.TrimSpace(k)] = val
	return nil
}

func cmdRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	addr := fs.String("addr", defaultAddr, "daemon 地址")
	tokenFile := fs.String("token-file", defaultToken(), "token 文件")
	turn := fs.String("turn", "", "本轮标识")
	etag := fs.String("etag", "", "发送 If-None-Match")
	noToken := fs.Bool("no-token", false, "不发送 token")
	ctx := ctxFlag{}
	fs.Var(ctx, "ctx", "context 字段，可重复：--ctx chat.user.nickname=小兔")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"addr": true, "token-file": true, "turn": true, "etag": true, "ctx": true,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("用法: ph render <id@version> [--ctx k=v]...")
	}
	if _, err := pack.ParseRef(fs.Arg(0)); err != nil {
		return err
	}

	q := url.Values{}
	q.Set("pack", fs.Arg(0))
	if *turn != "" {
		q.Set("turn", *turn)
	}
	for k, v := range ctx {
		q.Set("ctx."+k, v)
	}
	u := "http://" + *addr + "/v1/render?" + q.Encode()

	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if !*noToken {
		tok, err := loadOrCreateToken(*tokenFile)
		if err != nil {
			return err
		}
		req.Header.Set("X-Ph-Token", tok)
	}
	if *etag != "" {
		req.Header.Set("If-None-Match", *etag)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	fmt.Printf("HTTP %d %s\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	for _, h := range []string{"ETag", "X-Pack-Revs", "X-Tree-Hash", "X-Render-Ms", "X-Ph-Warnings"} {
		if v := resp.Header.Get(h); v != "" {
			fmt.Printf("%-15s %s\n", h+":", v)
		}
	}
	fmt.Println("---")
	if resp.StatusCode == http.StatusNotModified {
		fmt.Println("（304：内容未变，无需正文）")
		return nil
	}
	fmt.Print(string(body))
	if !strings.HasSuffix(string(body), "\n") {
		fmt.Println()
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("请求失败: %s", resp.Status)
	}
	return nil
}

func cmdHub(args []string) error {
	if len(args) < 1 || args[0] != "serve" {
		return fmt.Errorf("用法: ph hub serve [--addr HOST] [--root DIR] [--publish-token T]")
	}
	fs := flag.NewFlagSet("hub serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:18500", "监听地址")
	root := fs.String("root", filepath.Join(homeDir(), "hub"), "注册表存储根目录")
	token := fs.String("publish-token", "", "发布 token（空=不校验，仅本机调试）")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	reg := hub.NewRegistry(*root)
	idx, err := reg.Index()
	if err != nil {
		return err
	}
	fmt.Printf("prompt-hub registry  listening on http://%s\n", *addr)
	fmt.Printf("  root    %s\n", *root)
	fmt.Printf("  已发布  %d 个版本\n", len(idx))
	if *token == "" {
		fmt.Println("  发布    (无 token 校验，只适合本机调试)")
	}
	return http.ListenAndServe(*addr, (&hub.Server{Reg: reg, PublishToken: *token}).Handler())
}

func cmdPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	hubURL := fs.String("hub", "", "hub 地址")
	token := fs.String("token", "", "发布 token")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"hub": true, "token": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("用法: ph publish <pack 路径> --hub URL")
	}
	if *hubURL == "" {
		return fmt.Errorf("必须指定 --hub")
	}
	meta, err := hub.NewClient(*hubURL, *token).Publish(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Printf("已发布 %s\n  tree hash  %s\n  大小       %d 字节\n", meta.Ref(), meta.TreeHash, meta.Size)
	if len(meta.Manifest.Requires) > 0 {
		fmt.Printf("  声明读     %s\n", strings.Join(meta.Manifest.Requires, ", "))
	}
	if len(meta.Manifest.Egress) > 0 {
		fmt.Printf("  声明出网   %s\n", strings.Join(meta.Manifest.Egress, ", "))
	}
	return nil
}

func cmdIndex(args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	hubURL := fs.String("hub", "", "hub 地址")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hubURL == "" {
		return fmt.Errorf("必须指定 --hub")
	}
	packs, err := hub.NewClient(*hubURL, "").Index()
	if err != nil {
		return err
	}
	if len(packs) == 0 {
		fmt.Println("（注册表为空）")
		return nil
	}
	for _, m := range packs {
		var caps []string
		if n := len(m.Manifest.Requires); n > 0 {
			caps = append(caps, fmt.Sprintf("读 %d 字段", n))
		}
		if n := len(m.Manifest.Egress); n > 0 {
			caps = append(caps, fmt.Sprintf("出网 %d 目标", n))
		}
		if n := len(m.Manifest.Includes); n > 0 {
			caps = append(caps, fmt.Sprintf("依赖 %d 个", n))
		}
		note := ""
		if len(m.MissingDeps) > 0 {

			note = "  ⚠ 缺依赖: " + strings.Join(m.MissingDeps, ", ")
		}
		fmt.Printf("%-26s %-8s %-8s %7.1fKB  %s%s\n",
			m.Ref(), m.Manifest.Runtime, m.Manifest.Profile, float64(m.Size)/1024,
			strings.Join(caps, ", "), note)
	}
	return nil
}

func installFromHub(st *store.Store, hubURL string, ref pack.Ref, withDeps bool) error {
	return installFromHubRec(st, hub.NewClient(hubURL, ""), ref, withDeps, map[string]bool{})
}

func installFromHubRec(st *store.Store, client *hub.Client, ref pack.Ref, withDeps bool, seen map[string]bool) error {
	if seen[ref.String()] {
		return nil
	}
	seen[ref.String()] = true

	meta, archive, err := client.Fetch(ref.ID, ref.Version)
	if err != nil {
		return err
	}
	if len(meta.MissingDeps) > 0 {
		fmt.Printf("  注意 %s 在 hub 上缺依赖: %s\n", meta.Ref(), strings.Join(meta.MissingDeps, ", "))
	}

	tree, err := pack.ReadArchive(archive)
	if err != nil {
		return err
	}
	m, err := tree.Manifest()
	if err != nil {
		return err
	}
	if withDeps {
		lock, err := pack.LockFromTree(tree)
		if err != nil {
			return err
		}
		depRefs, err := m.ExactRefs(lock)
		if err != nil {
			return fmt.Errorf("这个包在 hub 上的依赖无法解析: %w", err)
		}
		for _, depRef := range depRefs {
			if _, _, err := st.Resolve(depRef.ID, depRef.Version); err == nil {
				continue
			}
			fmt.Printf("  依赖 %s 不在本地，从 hub 安装\n", depRef)
			if err := installFromHubRec(st, client, depRef, withDeps, seen); err != nil {
				return fmt.Errorf("安装依赖 %s 失败: %w", depRef, err)
			}
		}
	}

	dir, err := st.Install(&pack.LoadResult{Manifest: m, Tree: tree})
	if err != nil {
		return err
	}
	fmt.Printf("已从 hub 安装 %s@%s\n  tree hash  %s（与 hub 元数据一致）\n  目录       %s\n",
		m.PackID, m.Version, meta.TreeHash, dir)
	return nil
}

func cmdUpgrade(args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	storeDir := fs.String("store", defaultStore(), "store 目录")
	hubURL := fs.String("hub", "", "hub 地址")
	yes := fs.Bool("yes", false, "同意一次扩大能力的升级")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"store": true, "hub": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("用法: ph upgrade <packId> --hub URL [--yes]")
	}
	if *hubURL == "" {
		return fmt.Errorf("必须指定 --hub")
	}
	id := fs.Arg(0)
	st := store.New(*storeDir)

	versions, err := st.Installed(id)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		return fmt.Errorf("%s 未安装", id)
	}

	curVer := versions[0]
	for _, v := range versions[1:] {
		if pack.CompareVersions(v, curVer) > 0 {
			curVer = v
		}
	}
	curDir, _, err := st.Resolve(id, curVer)
	if err != nil {
		return err
	}
	curLoaded, err := pack.Load(curDir)
	if err != nil {
		return err
	}

	meta, err := hub.NewClient(*hubURL, "").Latest(id)
	if err != nil {
		return err
	}
	if pack.CompareVersions(meta.Manifest.Version, curVer) <= 0 {
		fmt.Printf("%s 已是最新（本地 %s，hub %s）\n", id, curVer, meta.Manifest.Version)
		return nil
	}

	diff := hub.DiffCapabilities(*curLoaded.Manifest, meta.Manifest)
	fmt.Println(diff.String())
	if diff.Widening() && !*yes {
		return fmt.Errorf("这次升级**扩大了能力**（见上），需要 --yes 明确同意")
	}
	return installFromHub(st, *hubURL, pack.Ref{ID: id, Version: meta.Manifest.Version}, true)
}

func cmdRuntime(args []string) error {
	if len(args) == 0 {
		tiers := tierStates(homeDir(), "")
		fmt.Println("档位        状态")
		for _, t := range tiers {
			if t.Available {
				fmt.Printf("  %-9s ✅ %s\n", t.Name, t.Detail)
				continue
			}
			fmt.Printf("  %-9s ❌ %s\n", t.Name, t.Detail)
			if t.Enable != "" {
				fmt.Printf("            %s\n", t.Enable)
			}
		}
		fmt.Println()
		fmt.Println("包用哪个档位由 pack.yaml 的 runtime 决定。宿主没启用的档位会被**明确拒绝**，")
		fmt.Println("不会静默降级 —— 静默降级等于渲染出一份你不知道自己错了的 prompt。")
		fmt.Println("template 与 wasm 是内置档，不需要任何外部可执行文件。")
		return nil
	}
	if args[0] != "enable" {
		return fmt.Errorf("用法: ph runtime [enable <档位>]")
	}
	if len(args) != 2 {
		return fmt.Errorf("用法: ph runtime enable <template|wasm|php>")
	}
	switch args[1] {
	case "template", "wasm":
		fmt.Printf("%s 是内置档位，不需要启用\n", args[1])
		return nil
	case "php":
		return enablePHP()
	default:
		return fmt.Errorf("未知档位 %q（可用：template, wasm, php）", args[1])
	}
}

func enablePHP() error {
	dest := filepath.Join(homeDir(), "tools", "php")
	fmt.Printf("把便携 PHP 装到 %s\n", dest)
	version, err := phpfetch.Install(dest, os.Stdout, nil)
	if err != nil {
		return err
	}
	fmt.Printf("已安装 PHP %s\n", version)

	if bin, reason := render.FindPHP("", homeDir()); bin != "" {
		fmt.Printf("php 档现在可用：%s\n", bin)
	} else {
		return fmt.Errorf("装完了但 FindPHP 仍找不到：%s", reason)
	}
	return nil
}

func defaultProfilesDir() string { return filepath.Join(homeDir(), "profiles") }

func cmdProfile(args []string) error {
	fs := flag.NewFlagSet("profile", flag.ExitOnError)
	profilesDir := fs.String("profiles", defaultProfilesDir(), "额外的 profile 目录")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"profiles": true})); err != nil {
		return err
	}
	reg, err := profile.NewRegistry(*profilesDir)
	if err != nil {
		return err
	}
	if fs.NArg() == 0 {
		for _, n := range reg.Names() {
			p, _ := reg.Get(n)
			fmt.Printf("%-12s %2d 个字段  %s\n", p.Name, len(p.Fields), p.Description)
		}
		fmt.Printf("\n用法：ph profile <name/version>\n")
		return nil
	}
	p, ok := reg.Get(fs.Arg(0))
	if !ok {
		return fmt.Errorf("没有 profile %s；可用：%s", fs.Arg(0), strings.Join(reg.Names(), ", "))
	}
	fmt.Printf("%s  %s\n\n", p.Name, p.Description)
	fmt.Printf("%-28s %-10s %-12s %-9s %s\n", "字段", "类型", "稳定性", "隐私", "说明")
	for _, f := range p.Fields {
		fmt.Printf("%-28s %-10s %-12s %-9s %s\n", f.Name, f.Type, f.Stability, f.Privacy, f.Desc)
	}
	fmt.Println()
	fmt.Println("稳定性：static=可全局冻结前缀  session=按会话冻结  per-request=必须 append-only")
	fmt.Println("隐私：  pii=含个人数据，发给远端模型前应当让用户可见")
	return nil
}

func cmdLock(args []string) error {
	fs := flag.NewFlagSet("lock", flag.ExitOnError)
	storeDir := fs.String("store", defaultStore(), "store 目录")
	update := fs.Bool("update", false, "把范围升到当前可用的最高版本")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"store": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("用法: ph lock <pack 路径> [--update]")
	}
	dir := fs.Arg(0)
	loaded, err := pack.Load(dir)
	if err != nil {
		return err
	}
	if err := loaded.Manifest.Validate(); err != nil {
		return fmt.Errorf("manifest 不合法: %w", err)
	}
	if loaded.SingleFile {
		return fmt.Errorf("单文件 pack 没有目录可放 %s", pack.LockName)
	}
	if len(loaded.Manifest.Includes) == 0 {
		fmt.Println("这个 pack 没有依赖，不需要锁定")
		return nil
	}

	resolver := &store.Resolver{Store: store.New(*storeDir)}
	old, err := pack.LockFromTree(loaded.Tree)
	if err != nil {
		return err
	}

	next := &pack.Lock{}
	for _, inc := range loaded.Manifest.Includes {
		c, err := pack.ParseInclude(inc)
		if err != nil {
			return err
		}
		pick, kept := "", false
		switch {
		case !c.IsRange():
			pick = c.Exact
		case !*update:

			if e, ok := old.Get(c.ID); ok {
				if ref, err := pack.ParseRef(e.Ref); err == nil && c.Satisfies(ref.Version) {
					pick, kept = ref.Version, true
				}
			}
		}
		if pick == "" {
			for _, v := range resolver.Versions(c.ID) {
				if !c.Satisfies(v) {
					continue
				}
				if pick == "" || pack.CompareVersions(v, pick) > 0 {
					pick = v
				}
			}
			if pick == "" {
				return fmt.Errorf("依赖 %s 找不到满足的版本（store 与内嵌层里都没有）；先 ph install", inc)
			}
		}
		resolved, err := resolver.Resolve(c.ID, pick)
		if err != nil {
			return fmt.Errorf("依赖 %s@%s 不可用: %w", c.ID, pick, err)
		}
		next.Set(c.ID, pick, resolved.TreeHash)

		note := ""
		if c.IsRange() {
			note = "（范围 " + c.Spec
			if kept {
				note += "，沿用已有锁定"
			} else if *update {
				note += "，已更新"
			}
			note += "）"
		}
		fmt.Printf("  %-14s %-10s %s%s\n", c.ID, pick, resolved.TreeHash, note)
	}

	raw, err := next.Marshal()
	if err != nil {
		return err
	}
	dst := filepath.Join(dir, pack.LockName)
	if err := os.WriteFile(dst, raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("已写入 %s（%d 条）\n", dst, len(next.Entries))
	return nil
}

func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	storeDir := fs.String("store", defaultStore(), "store 目录")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"store": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("用法: ph remove <id@version>")
	}
	ref, err := pack.ParseRef(fs.Arg(0))
	if err != nil {
		return err
	}
	if err := store.New(*storeDir).Remove(ref.ID, ref.Version); err != nil {
		return err
	}
	fmt.Printf("已从 store 删除 %s\n", ref)
	return nil
}

func cmdGC(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ExitOnError)
	storeDir := fs.String("store", defaultStore(), "store 目录")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"store": true})); err != nil {
		return err
	}
	removed, err := store.New(*storeDir).GC()
	if err != nil {
		return err
	}
	if len(removed) == 0 {
		fmt.Println("没有可清理的残留")
		return nil
	}
	for _, p := range removed {
		fmt.Printf("  清理 %s\n", strings.TrimPrefix(p, *storeDir+string(os.PathSeparator)))
	}
	fmt.Printf("共清理 %d 项\n", len(removed))
	return nil
}

func cmdExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	storeDir := fs.String("store", defaultStore(), "store 目录")
	to := fs.String("to", "", "输出目录")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"store": true, "to": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 || *to == "" {
		return fmt.Errorf("用法: ph export <id@version> --to DIR")
	}
	ref, err := pack.ParseRef(fs.Arg(0))
	if err != nil {
		return err
	}
	dir, treeHash, err := store.New(*storeDir).Resolve(ref.ID, ref.Version)
	if err != nil {
		return err
	}
	loaded, err := pack.Load(dir)
	if err != nil {
		return err
	}

	dst := filepath.Join(*to, store.EmbedDirName(ref.ID, ref.Version))
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for rel, content := range loaded.Tree {
		abs := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(abs, content, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("已导出 %s 到 %s（内嵌层布局）\n  tree hash  %s\n", ref, dst, treeHash)
	fmt.Printf("  用法：ph serve --embed %s\n", *to)
	return nil
}
