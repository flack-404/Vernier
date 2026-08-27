//! Global allocator and panic handler for `wasm32-unknown-unknown`.
//!
//! `no_std` targets require an explicit allocator. We use `dlmalloc` which
//! is small, well-tested, and has a proven WASM track record.

/// The WASM build is `no_std` and must bring its own allocator. Under `cargo test`
/// the host target links `std`, which already has one, so this is gated too — and
/// the import along with it, or the test build warns about an unused import.
#[cfg(all(not(test), target_arch = "wasm32"))]
#[global_allocator]
static ALLOCATOR: dlmalloc::GlobalDlmalloc = dlmalloc::GlobalDlmalloc;

/// Panic handler — required for `no_std`.
/// WASM traps on `unreachable`, which surfaces as a wazero error in Go.
///
/// Gated out under `cfg(test)` so `cargo test` can build for the host target,
/// where `std` supplies its own panic handler and two would collide. The upstream
/// baseline leaves this ungated, which is why its own unit tests cannot be run;
/// the fix is its README's own suggestion.
#[cfg(all(not(test), target_arch = "wasm32"))]
#[panic_handler]
fn panic(_info: &core::panic::PanicInfo) -> ! {
    core::arch::wasm32::unreachable()
}
