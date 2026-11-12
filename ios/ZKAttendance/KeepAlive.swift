import AVFoundation

/// يخلي البرنامج شغال لما تطلع من التطبيق أو تقفل الموبايل، حتى الأجهزة الثانية تبقى تدخل.
/// iOS يجمّد أي تطبيق بالخلفية بعد ثواني إلا اللي يشغّل صوت، فنشغّل صوت صامت يندمج ويا
/// صوت التطبيقات الثانية (ما يوقف الأغاني ولا يطلع بشاشة القفل).
/// السحب لفوق (إغلاق التطبيق) يوقفه بكل الأحوال.
final class KeepAlive: NSObject {
    static let shared = KeepAlive()

    private var player: AVAudioPlayer?
    private(set) var lastError: String?

    var isRunning: Bool { player?.isPlaying ?? false }

    private override init() {
        super.init()
        let center = NotificationCenter.default
        // مكالمة أو منبّه يوقف الصوت، فنرجّعه أول ما يخلصون
        center.addObserver(self, selector: #selector(interrupted(_:)),
                           name: AVAudioSession.interruptionNotification, object: nil)
        center.addObserver(self, selector: #selector(mediaServicesReset),
                           name: AVAudioSession.mediaServicesWereResetNotification, object: nil)
    }

    /// يشغّله (أو يتأكد إنه شغال). يرجّع false إذا ما گدر.
    @discardableResult
    func start() -> Bool {
        do {
            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.playback, mode: .default, options: [.mixWithOthers])
            try session.setActive(true)
            if player == nil {
                let silent = try AVAudioPlayer(data: KeepAlive.silence())
                silent.numberOfLoops = -1
                silent.prepareToPlay()
                player = silent
            }
            guard let current = player else { return false }
            if !current.isPlaying && !current.play() {
                lastError = "play failed"
                return false
            }
            lastError = nil
            return true
        } catch {
            lastError = error.localizedDescription
            return false
        }
    }

    @objc private func interrupted(_ note: Notification) {
        guard let raw = note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
              let type = AVAudioSession.InterruptionType(rawValue: raw),
              type == .ended else { return }
        DispatchQueue.main.async { self.start() }
    }

    @objc private func mediaServicesReset() {
        DispatchQueue.main.async {
            self.player = nil
            self.start()
        }
    }

    /// ثانية صامتة بصيغة WAV (8000 عينة، 16 بت، قناة وحدة)
    private static func silence() -> Data {
        let rate: UInt32 = 8000
        let size: UInt32 = rate * 2
        var b: [UInt8] = []
        func text(_ s: String) { b.append(contentsOf: Array(s.utf8)) }
        func u32(_ v: UInt32) { b.append(contentsOf: [UInt8(v & 0xff), UInt8((v >> 8) & 0xff), UInt8((v >> 16) & 0xff), UInt8((v >> 24) & 0xff)]) }
        func u16(_ v: UInt16) { b.append(contentsOf: [UInt8(v & 0xff), UInt8((v >> 8) & 0xff)]) }
        text("RIFF"); u32(36 + size); text("WAVE")
        text("fmt "); u32(16); u16(1); u16(1); u32(rate); u32(rate * 2); u16(2); u16(16)
        text("data"); u32(size)
        b.append(contentsOf: [UInt8](repeating: 0, count: Int(size)))
        return Data(b)
    }
}
