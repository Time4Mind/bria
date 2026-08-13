import Darwin
import Foundation
import Speech

private struct Options {
    var authorize = false
    var input: String?
    var locale = "auto"
    var onDevice = false
}

private final class AuthorizationState: @unchecked Sendable {
    private let lock = NSLock()
    private var status: SFSpeechRecognizerAuthorizationStatus = .notDetermined

    func store(_ value: SFSpeechRecognizerAuthorizationStatus) {
        lock.lock()
        status = value
        lock.unlock()
    }

    func load() -> SFSpeechRecognizerAuthorizationStatus {
        lock.lock()
        defer { lock.unlock() }
        return status
    }
}

private final class RecognitionState: @unchecked Sendable {
    private let lock = NSLock()
    private var finished = false
    private var transcript = ""
    private var failed = false

    func accept(_ result: SFSpeechRecognitionResult?, error: Error?) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        guard !finished else { return false }
        if let result = result {
            transcript = result.bestTranscription.formattedString
            finished = result.isFinal
        } else if error != nil {
            failed = true
            finished = true
        }
        return finished
    }

    func snapshot() -> (String, Bool) {
        lock.lock()
        defer { lock.unlock() }
        return (transcript, failed)
    }
}

private func fail(_ message: String, code: Int32 = 1) -> Never {
    let data = Data(("bria-apple-speech: " + message + "\n").utf8)
    try? FileHandle.standardError.write(contentsOf: data)
    exit(code)
}

private func parseArguments() -> Options {
    var options = Options()
    let arguments = Array(CommandLine.arguments.dropFirst())
    var index = 0
    while index < arguments.count {
        switch arguments[index] {
        case "--authorize":
            options.authorize = true
        case "--on-device":
            options.onDevice = true
        case "--input", "--locale":
            guard index + 1 < arguments.count else {
                fail("missing value for \(arguments[index])", code: 2)
            }
            if arguments[index] == "--input" {
                options.input = arguments[index + 1]
            } else {
                options.locale = arguments[index + 1]
            }
            index += 1
        default:
            fail("unsupported argument", code: 2)
        }
        index += 1
    }
    if options.authorize && (options.input != nil || arguments.count != 1) {
        fail("--authorize must be used alone", code: 2)
    }
    return options
}

private func authorization(requestIfNeeded: Bool) -> SFSpeechRecognizerAuthorizationStatus {
    let current = SFSpeechRecognizer.authorizationStatus()
    guard current == .notDetermined && requestIfNeeded else { return current }
    let semaphore = DispatchSemaphore(value: 0)
    let state = AuthorizationState()
    SFSpeechRecognizer.requestAuthorization { status in
        state.store(status)
        semaphore.signal()
    }
    guard semaphore.wait(timeout: .now() + 60) == .success else { return .notDetermined }
    return state.load()
}

private func requireAuthorization(requestIfNeeded: Bool) {
    switch authorization(requestIfNeeded: requestIfNeeded) {
    case .authorized:
        return
    case .notDetermined:
        fail("speech recognition is not authorized; run with --authorize first")
    case .denied:
        fail("speech recognition permission was denied")
    case .restricted:
        fail("speech recognition is restricted on this Mac")
    @unknown default:
        fail("unknown speech recognition authorization state")
    }
}

private func recognize(options: Options) {
    guard options.onDevice else { fail("network-capable recognition is disabled") }
    guard let input = options.input, FileManager.default.fileExists(atPath: input) else {
        fail("input file is unavailable", code: 2)
    }
    requireAuthorization(requestIfNeeded: false)
    let locale = options.locale == "auto" ? Locale.current : Locale(identifier: options.locale)
    guard let recognizer = SFSpeechRecognizer(locale: locale) else {
        fail("the requested locale is unsupported")
    }
    guard recognizer.supportsOnDeviceRecognition else {
        fail("on-device recognition is unavailable for the requested locale")
    }
    guard recognizer.isAvailable else { fail("speech recognizer is unavailable") }

    let request = SFSpeechURLRecognitionRequest(url: URL(fileURLWithPath: input))
    request.requiresOnDeviceRecognition = true
    request.shouldReportPartialResults = false
    if #available(macOS 13.0, *) { request.addsPunctuation = true }

    let semaphore = DispatchSemaphore(value: 0)
    let state = RecognitionState()
    let task = recognizer.recognitionTask(with: request) { result, error in
        if state.accept(result, error: error) {
            semaphore.signal()
        }
    }
    if semaphore.wait(timeout: .now() + 110) != .success {
        task.cancel()
        fail("recognition timed out")
    }
    let (transcript, failed) = state.snapshot()
    if failed { fail("recognition failed") }
    let output = transcript.trimmingCharacters(in: .whitespacesAndNewlines)
    if output.isEmpty { fail("recognition returned no text") }
    print(output)
}

private let options = parseArguments()
if options.authorize {
    requireAuthorization(requestIfNeeded: true)
    print("authorized")
} else {
    recognize(options: options)
}
