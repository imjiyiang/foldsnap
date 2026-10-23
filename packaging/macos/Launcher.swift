import AppKit
import Darwin

// Finder needs a persistent application delegate: a running CLI alone does not
// receive reopen events, display startup errors, or provide a menu-bar entry.
final class Launcher: NSObject, NSApplicationDelegate {
    var item: NSStatusItem!
    var service: Process?
    var quitItem: NSMenuItem?
    var browserURL: URL?
    var pending = Data()
    var diagnostics = ""
    var quitting = false
    var started = false
    var startupWindow: NSWindow?
    var startupLabel: NSTextField?
    var startupTimer: Timer?
    let launchTime = Date()

    func showStartup() {
        if startupWindow == nil {
            let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 440, height: 170), styleMask: [.titled, .closable], backing: .buffered, defer: false)
            window.title = "Foldsnap · 留档"
            window.isReleasedWhenClosed = false
            window.center()
            let title = NSTextField(labelWithString: "正在打开 Foldsnap")
            title.font = .systemFont(ofSize: 21, weight: .semibold)
            title.frame = NSRect(x: 28, y: 112, width: 390, height: 30)
            let label = NSTextField(wrappingLabelWithString: "正在启动本地服务，完成后会自动打开备份界面。")
            label.frame = NSRect(x: 28, y: 40, width: 385, height: 60)
            label.textColor = .secondaryLabelColor
            window.contentView?.addSubview(title)
            window.contentView?.addSubview(label)
            startupLabel = label
            startupWindow = window
        }
        startupWindow?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    func record(_ message: String) {
        let safe = message.replacingOccurrences(of: "(#token=|Bearer )[A-Za-z0-9_-]+", with: "$1[已隐藏]", options: .regularExpression)
        diagnostics = String((diagnostics + safe).suffix(16000))
        let dir = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Logs/LocalBackup")
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        try? diagnostics.write(to: dir.appendingPathComponent("startup.log"), atomically: true, encoding: .utf8)
    }
    var terminationSignals: [DispatchSourceSignal] = []

    func applicationDidFinishLaunching(_ notification: Notification) {
        item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        if let path = Bundle.main.path(forResource: "StatusIcon", ofType: "png"), let image = NSImage(contentsOfFile: path) {
            image.size = NSSize(width: 19, height: 19)
            image.isTemplate = false
            item.button?.image = image
        } else {
            item.button?.title = "留档"
        }
        item.button?.toolTip = "Foldsnap 正在启动"
        let menu = NSMenu()
        let open = menu.addItem(withTitle: "打开 Foldsnap · 留档", action: #selector(openUI), keyEquivalent: "o")
        open.target = self
        menu.addItem(.separator())
        let quit = menu.addItem(withTitle: "退出 Foldsnap（停止服务）", action: #selector(quitApp), keyEquivalent: "q")
        quit.target = self
        quitItem = quit
        item.menu = menu
        for number in [SIGTERM, SIGINT] {
            signal(number, SIG_IGN)
            let source = DispatchSource.makeSignalSource(signal: number, queue: .main)
            source.setEventHandler { NSApp.terminate(nil) }
            source.resume()
            terminationSignals.append(source)
        }
        showStartup()
        record("Launcher started at \(Date())\n")
        startupTimer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            guard let self = self, !self.started else { return }
            let seconds = Int(Date().timeIntervalSince(self.launchTime))
            if seconds >= 15 {
                self.startupLabel?.stringValue = "本地服务仍在启动（\(seconds) 秒）。\n可从菜单栏退出后重试；诊断日志：~/Library/Logs/LocalBackup/startup.log"
            }
        }
        launchService()
    }

    func launchService() {
        let process = Process()
        process.executableURL = Bundle.main.executableURL?.deletingLastPathComponent().appendingPathComponent("localbackup-service")
        process.arguments = ["-open=false", "-auto-port=true"]
        if let marker = CommandLine.arguments.firstIndex(of: "--") {
            process.arguments! += Array(CommandLine.arguments.dropFirst(marker + 1))
        }
        let output = Pipe()
        let errors = Pipe()
        process.standardOutput = output
        process.standardError = errors
        output.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            if data.isEmpty { handle.readabilityHandler = nil; return }
            DispatchQueue.main.async { self?.consume(data) }
        }
        errors.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            if data.isEmpty { handle.readabilityHandler = nil; return }
            let message = String(decoding: data, as: UTF8.self)
            DispatchQueue.main.async {
                self?.record(message)
            }
        }
        process.terminationHandler = { [weak self] child in
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.25) {
                guard let self = self else { return }
                if self.quitting { return }
                // A second launch attaches to the already authenticated service.
                if child.terminationStatus == 0 && self.started {
                    self.quitItem?.title = "退出菜单栏入口（后台服务继续运行）"
                    return
                }
                self.fail("后台服务已退出（\(child.terminationStatus)）。\n" + self.diagnostics)
            }
        }
        service = process
        do { try process.run() }
        catch { fail("无法启动后台服务：\(error.localizedDescription)") }
    }

    func consume(_ data: Data) {
        pending.append(data)
        while let newline = pending.firstIndex(of: 10) {
            let line = String(decoding: pending[..<newline], as: UTF8.self)
            pending.removeSubrange(...newline)
            if let prefix = ["Foldsnap 已启动：", "LocalBackup 已启动："].first(where: { line.hasPrefix($0) }), let url = URL(string: String(line.dropFirst(prefix.count))),
               url.scheme == "http", let host = url.host,
               host == "127.0.0.1" || host == "::1" {
                browserURL = url
                started = true
                startupTimer?.invalidate()
                startupWindow?.orderOut(nil)
                record("Browser ready after \(String(format: "%.3f", Date().timeIntervalSince(launchTime))) seconds\n")
                item.button?.toolTip = "Foldsnap 正在运行"
                openUI()
            }
        }
    }

    @objc func openUI() {
        guard let url = browserURL else { showStartup(); return }
        NSWorkspace.shared.open(url, configuration: NSWorkspace.OpenConfiguration()) { _, error in
            DispatchQueue.main.async { self.record("Browser open callback after \(String(format: "%.3f", Date().timeIntervalSince(self.launchTime))) seconds; success=\(error == nil)\n") }
            if let error = error {
                DispatchQueue.main.async {
                    let alert = NSAlert()
                    alert.messageText = "无法打开浏览器"
                    alert.informativeText = error.localizedDescription
                    NSApp.activate(ignoringOtherApps: true)
                    alert.runModal()
                }
            }
        }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        openUI()
        return false
    }

    func fail(_ message: String) {
        guard !quitting else { return }
        let safe = message.replacingOccurrences(of: "(#token=|Bearer )[A-Za-z0-9_-]+", with: "$1[已隐藏]", options: .regularExpression)
        let log = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Logs/LocalBackup")
        try? FileManager.default.createDirectory(at: log, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        try? safe.write(to: log.appendingPathComponent("startup-error.log"), atomically: true, encoding: .utf8)
        startupTimer?.invalidate()
        startupWindow?.orderOut(nil)
        let alert = NSAlert()
        alert.alertStyle = .critical
        alert.messageText = "Foldsnap 启动失败"
        alert.informativeText = safe + "\n\n错误日志：~/Library/Logs/LocalBackup/startup-error.log"
        alert.addButton(withTitle: "退出")
        NSApp.activate(ignoringOtherApps: true)
        alert.runModal()
        NSApp.terminate(nil)
    }

    @objc func quitApp() { NSApp.terminate(nil) }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        quitting = true
        if let service = service, service.isRunning {
            service.terminate()
            // The Go server has a 15-second shutdown deadline. Waiting here also
            // reaps the child before AppKit tears down its dispatch callbacks.
            service.waitUntilExit()
            return .terminateNow
        }
        return .terminateNow
    }
}

let app = NSApplication.shared
let delegate = Launcher()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
