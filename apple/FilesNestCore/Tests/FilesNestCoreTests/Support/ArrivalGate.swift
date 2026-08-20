import Foundation

/// Deterministically proves N tasks run concurrently. The first `target`
/// callers to `enter()` block until all `target` have arrived, so `peak`
/// reaches exactly `target`; once opened, later callers pass straight through.
actor ArrivalGate {
    private let target: Int
    private var current = 0
    private(set) var peak = 0
    private var opened = false
    private var waiters: [CheckedContinuation<Void, Never>] = []

    init(target: Int) { self.target = target }

    func enter() async {
        current += 1
        peak = max(peak, current)
        if opened { return }
        if current >= target {
            opened = true
            for w in waiters { w.resume() }
            waiters.removeAll()
            return
        }
        await withCheckedContinuation { waiters.append($0) }
    }

    func exit() { current -= 1 }
}
