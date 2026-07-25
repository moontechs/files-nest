import Testing
import Foundation
@testable import FilesNestCore

@Suite struct StreamDiagnosticsTests {

    @Test func recordsBlobStats() {
        let d = StreamDiagnostics()
        d.enter(byteCount: 100); d.exit()
        d.enter(byteCount: 300); d.exit()
        #expect(d.blobCount == 2)
        #expect(d.totalBytes == 400)
        #expect(d.minBlob == 100)
        #expect(d.maxBlob == 300)
    }

    @Test func maxConcurrentIsOneForSequentialDelivery() {
        let d = StreamDiagnostics()
        for _ in 0..<10 { d.enter(byteCount: 8); d.exit() }
        #expect(d.maxConcurrent == 1)
    }

    /// If two threads are inside `enter`/`exit` at once, maxConcurrent must
    /// exceed 1. Without this the counter could be inert and always report 1 —
    /// the exact false "good news" spec §8.1 warns about.
    @Test func maxConcurrentDetectsOverlap() {
        let d = StreamDiagnostics()
        let bothEntered = DispatchSemaphore(value: 0)
        let release = DispatchSemaphore(value: 0)
        let done = DispatchSemaphore(value: 0)
        for _ in 0..<2 {
            Thread.detachNewThread {
                d.enter(byteCount: 8)
                bothEntered.signal()
                release.wait()      // hold both inside enter/exit simultaneously
                d.exit()
                done.signal()
            }
        }
        bothEntered.wait(); bothEntered.wait()   // both are now inside
        release.signal(); release.signal()
        done.wait(); done.wait()
        #expect(d.maxConcurrent == 2)
    }
}
