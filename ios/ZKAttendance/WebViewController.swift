import UIKit
import WebKit
import Mobile

/// يشغّل البرنامج (Go) داخل التطبيق ويعرضه بمتصفح مدمج.
/// ما نفتح Safari لأن iOS يجمّد التطبيق بالخلفية، فيوقف البرنامج.
final class WebViewController: UIViewController, WKNavigationDelegate, WKUIDelegate, WKScriptMessageHandler {
    private var webView: WKWebView!
    private var serverURL: URL?
    private var loadFailures = 0
    private var selfTestStarted = false
    private var testLog: [String: Any] = [:]

    /// اختبار تلقائي على المحاكي: -zkSelfTest <IP جهاز البصمة>
    private let selfTestIP: String? = {
        let args = ProcessInfo.processInfo.arguments
        if let i = args.firstIndex(of: "-zkSelfTest"), i + 1 < args.count { return args[i + 1] }
        return nil
    }()

    /// ملفات البرنامج تنحفظ هنا، وتبين بتطبيق "الملفات"
    private var documentsURL: URL {
        FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
    }

    /// شريط الساعة والبطارية فوق: غامق بلون رأس البرنامج، والكتابة بيضاء
    override var preferredStatusBarStyle: UIStatusBarStyle { .lightContent }

    override func viewDidLoad() {
        super.viewDidLoad()
        view.backgroundColor = UIColor(red: 22 / 255, green: 33 / 255, blue: 46 / 255, alpha: 1)

        let config = WKWebViewConfiguration()
        let controller = config.userContentController
        controller.add(self, name: "zkDownload")
        controller.add(self, name: "zkPrint")
        controller.add(self, name: "zkTest")
        controller.addUserScript(WKUserScript(source: Self.bridgeScript, injectionTime: .atDocumentStart, forMainFrameOnly: true))

        webView = WKWebView(frame: .zero, configuration: config)
        webView.translatesAutoresizingMaskIntoConstraints = false
        webView.navigationDelegate = self
        webView.uiDelegate = self
        webView.isOpaque = false
        webView.backgroundColor = UIColor(red: 0.957, green: 0.965, blue: 0.973, alpha: 1)
        view.addSubview(webView)
        // الصفحة تبدي تحت شريط الساعة، حتى ما تمر الكتابة من وراه
        NSLayoutConstraint.activate([
            webView.topAnchor.constraint(equalTo: view.safeAreaLayoutGuide.topAnchor),
            webView.leadingAnchor.constraint(equalTo: view.leadingAnchor),
            webView.trailingAnchor.constraint(equalTo: view.trailingAnchor),
            webView.bottomAnchor.constraint(equalTo: view.bottomAnchor),
        ])

        NotificationCenter.default.addObserver(self, selector: #selector(appBecameActive),
                                               name: UIApplication.didBecomeActiveNotification, object: nil)
        // التطبيق سيرفر للأجهزة الثانية طول ما هو مفتوح، فالشاشة ما تطفي وحدها
        UIApplication.shared.isIdleTimerDisabled = true
        startServer()
    }

    private func startServer() {
        var error: NSError?
        let address = MobileStart(documentsURL.path, TimeZone.current.identifier, &error)
        if let error = error {
            showMessage("ما گدر البرنامج يشتغل: \(error.localizedDescription)")
            return
        }
        guard let url = URL(string: address) else { return }
        serverURL = url
        webView.load(URLRequest(url: url))
    }

    /// إذا التطبيق بقى بالخلفية فترة طويلة ممكن iOS يوقف البرنامج، فنتأكد منه كل ما يرجع
    @objc private func appBecameActive() {
        UIApplication.shared.isIdleTimerDisabled = true
        guard let base = serverURL, let ping = URL(string: "api/session", relativeTo: base) else { return }
        var request = URLRequest(url: ping)
        request.timeoutInterval = 2
        URLSession.shared.dataTask(with: request) { [weak self] _, response, _ in
            if (response as? HTTPURLResponse)?.statusCode == 200 { return }
            DispatchQueue.main.async { self?.startServer() }
        }.resume()
    }

    // MARK: - تحميل الصفحة

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        loadFailures = 0
        guard let ip = selfTestIP else { return }
        if webView.url?.path == "/login" { // الاختبار يدخل أول (أول فتح: ينشئ الحساب)
            webView.evaluateJavaScript(Self.selfTestLoginScript, completionHandler: nil)
            return
        }
        if !selfTestStarted {
            selfTestStarted = true
            webView.evaluateJavaScript(Self.selfTestScript(ip: ip), completionHandler: nil)
        }
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        loadFailures += 1
        if loadFailures <= 3 {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { [weak self] in self?.startServer() }
        } else {
            showMessage("ما گدرت أفتح البرنامج: \(error.localizedDescription)")
        }
    }

    // MARK: - نوافذ التأكيد والتنبيه (المتصفح المدمج ما يعرضها وحده)

    private var topController: UIViewController {
        var top: UIViewController = self
        while let presented = top.presentedViewController { top = presented }
        return top
    }

    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
        if selfTestIP != nil {
            recordTest(["alert": message])
            completionHandler()
            return
        }
        let alert = UIAlertController(title: nil, message: message, preferredStyle: .alert)
        alert.addAction(UIAlertAction(title: "تمام", style: .default) { _ in completionHandler() })
        topController.present(alert, animated: true)
    }

    func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (Bool) -> Void) {
        if selfTestIP != nil {
            recordTest(["confirm": message])
            completionHandler(true)
            return
        }
        let alert = UIAlertController(title: nil, message: message, preferredStyle: .alert)
        alert.addAction(UIAlertAction(title: "إلغاء", style: .cancel) { _ in completionHandler(false) })
        alert.addAction(UIAlertAction(title: "تأكيد", style: .default) { _ in completionHandler(true) })
        topController.present(alert, animated: true)
    }

    // MARK: - رسائل الصفحة: التصدير والطباعة

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        switch message.name {
        case "zkDownload":
            handleDownload(message.body)
        case "zkPrint":
            printPage()
        case "zkTest":
            if let body = message.body as? [String: Any] { recordTest(body) }
        default:
            break
        }
    }

    /// الملفات المصدّرة (Excel، النسخ) تنفتح بقائمة المشاركة: حفظ بالملفات، واتساب، إيميل...
    private func handleDownload(_ body: Any) {
        guard let dict = body as? [String: Any],
              let name = dict["name"] as? String,
              let dataURL = dict["data"] as? String,
              let comma = dataURL.firstIndex(of: ","),
              let data = Data(base64Encoded: String(dataURL[dataURL.index(after: comma)...])) else { return }
        var fileName = name.replacingOccurrences(of: "/", with: "-")
        if fileName.isEmpty { fileName = "export.csv" }
        let fileURL = FileManager.default.temporaryDirectory.appendingPathComponent(fileName)
        do {
            try? FileManager.default.removeItem(at: fileURL)
            try data.write(to: fileURL)
        } catch {
            showMessage("ما گدرت أحفظ الملف: \(error.localizedDescription)")
            return
        }
        recordTest(["download": ["name": fileName, "bytes": data.count]])
        let share = UIActivityViewController(activityItems: [fileURL], applicationActivities: nil)
        if let popover = share.popoverPresentationController {
            popover.sourceView = view
            popover.sourceRect = CGRect(x: view.bounds.midX, y: view.bounds.midY, width: 1, height: 1)
            popover.permittedArrowDirections = []
        }
        topController.present(share, animated: true)
    }

    private func printPage() {
        let printer = UIPrintInteractionController.shared
        let info = UIPrintInfo(dictionary: nil)
        info.outputType = .general
        info.jobName = "تقارير الحضور"
        printer.printInfo = info
        printer.printFormatter = webView.viewPrintFormatter()
        if UIDevice.current.userInterfaceIdiom == .pad {
            printer.present(from: CGRect(x: view.bounds.midX, y: 80, width: 1, height: 1), in: view, animated: true, completionHandler: nil)
        } else {
            printer.present(animated: true, completionHandler: nil)
        }
    }

    private func showMessage(_ text: String) {
        let alert = UIAlertController(title: "تقارير الحضور", message: text, preferredStyle: .alert)
        alert.addAction(UIAlertAction(title: "تمام", style: .default))
        topController.present(alert, animated: true)
    }

    // MARK: - الاختبار التلقائي (للمحاكي فقط)

    private func recordTest(_ entry: [String: Any]) {
        guard selfTestIP != nil else { return }
        testLog.merge(entry) { _, new in new }
        if let data = try? JSONSerialization.data(withJSONObject: testLog, options: [.prettyPrinted, .sortedKeys]) {
            try? data.write(to: documentsURL.appendingPathComponent("selftest.json"))
        }
    }

    /// يربط الصفحة بالتطبيق: التصدير يفتح قائمة المشاركة، والطباعة تفتح طباعة iOS
    static let bridgeScript = """
    (function () {
      var post = function (name, body) { window.webkit.messageHandlers[name].postMessage(body); };
      var originalClick = HTMLAnchorElement.prototype.click;
      HTMLAnchorElement.prototype.click = function () {
        if (this.download && /^(blob|data):/.test(this.href)) {
          var name = this.download;
          fetch(this.href).then(function (r) { return r.blob(); }).then(function (b) {
            var reader = new FileReader();
            reader.onload = function () { post('zkDownload', { name: name, data: reader.result }); };
            reader.readAsDataURL(b);
          });
          return;
        }
        return originalClick.call(this);
      };
      window.print = function () { post('zkPrint', ''); };
      document.addEventListener('DOMContentLoaded', function () {
        document.querySelectorAll('input[type=file]').forEach(function (input) { input.removeAttribute('accept'); });
      });
    })();
    """

    /// صفحة الدخول بالاختبار: بأول فتح تطلب إنشاء الحساب، فينشئ حساب تجربة ويدخل
    static let selfTestLoginScript = """
    (async function () {
      var post = function (o) { window.webkit.messageHandlers.zkTest.postMessage(o); };
      var wait = function (ms) { return new Promise(function (r) { setTimeout(r, ms); }); };
      var el = function (id) { return document.getElementById(id); };
      try {
        for (var i = 0; i < 20 && el('pw2').hidden; i++) await wait(250);
        var setup = !el('pw2').hidden;
        post({ login: setup ? 'setup' : 'login', loginTitle: el('title').textContent });
        el('user').value = 'selftest'; el('pw').value = 'selftest-2026'; el('pw2').value = 'selftest-2026';
        el('btn').click();
      } catch (e) { post({ error: 'login: ' + String(e) }); }
    })(); 0;
    """

    static func selfTestScript(ip: String) -> String {
        """
        (async function () {
          var post = function (o) { window.webkit.messageHandlers.zkTest.postMessage(o); };
          var wait = function (ms) { return new Promise(function (r) { setTimeout(r, ms); }); };
          var until = async function (fn, ms) { var end = Date.now() + (ms || 30000); while (Date.now() < end) { if (fn()) return true; await wait(250); } return false; };
          var el = function (id) { return document.getElementById(id); };
          var idle = function () { return !document.querySelector('[data-cmd=info]').disabled; };
          try {
            post({ loaded: document.title });
            var sh = await (await fetch('/api/share')).json();
            post({ shareMode: sh.mode, shareUrls: sh.urls });
            el('ip').value = '\(ip)';
            var t0 = Date.now();
            el('fetchBtn').click();
            await until(function () { return /^(تم|تعذر)/.test(el('status').textContent); }, 60000);
            post({ fetch: el('status').textContent, fetchMs: Date.now() - t0 });
            var t = await (await fetch('/api/cmd', { method: 'POST', headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ ip: '\(ip)', port: 4370, action: 'getTime' }) })).json();
            post({ goTime: t.pcTime || t.error, jsTime: new Date().toString(), jsZone: Intl.DateTimeFormat().resolvedOptions().timeZone });
            document.querySelector('[data-cmd=info]').click();
            await until(function () { return document.querySelectorAll('.info-grid div').length > 0 && idle(); });
            post({ info: el('cmdStatus').textContent, infoRows: document.querySelectorAll('.info-grid div').length });
            document.querySelector('[data-cmd=disable]').click();
            await until(function () { return /تم إيقاف/.test(el('cmdStatus').textContent) && idle(); });
            post({ disable: el('cmdStatus').textContent });
            document.querySelector('[data-cmd=enable]').click();
            await until(function () { return /تم تفعيل/.test(el('cmdStatus').textContent) && idle(); });
            post({ enable: el('cmdStatus').textContent });
            document.querySelector('[data-tab=summary]').click();
            await wait(500);
            post({ summaryRows: document.querySelectorAll('#tableArea tbody tr').length });
            window.scrollTo(0, document.querySelector('.tabs').getBoundingClientRect().top + window.scrollY - 20);
            await wait(5000);
            post({ done: true });
            el('csvBtn').click();
          } catch (e) { post({ error: String(e) }); }
        })(); 0;
        """
    }
}
