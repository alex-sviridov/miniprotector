import { ref, watch, onBeforeUnmount } from 'vue'

// Tracks whether the viewport is scrolled down to a bottom sentinel element
// via IntersectionObserver, rather than polling scroll events. Consumers
// (JobDetailView.vue) use `isFollowing` both to gate the jobs store's
// live-log eviction cap (only trim resident lines while the tail is
// actually being watched) and to decide whether to auto-scroll new lines
// into view or surface a "N new lines" affordance instead of yanking a
// deliberately-scrolled-up reader back down.
//
// `itemCount` must count only items appended at the tail -- pass a
// tail-activity counter, not a plain list length, or items inserted at the
// *front* (paged-in history) would be reported as new lines.
export function useAutoFollow(itemCount) {
  const sentinel = ref(null)
  const isFollowing = ref(true)
  const newLineCount = ref(0)
  let observer = null

  function attach(el) {
    observer?.disconnect()
    observer = null
    if (!el) return
    observer = new IntersectionObserver(
      ([entry]) => {
        isFollowing.value = entry.isIntersecting
        if (entry.isIntersecting) newLineCount.value = 0
      },
      // Slack below the viewport, not `threshold: 1.0`: every appended line
      // pushes the zero-height sentinel below the fold for a moment, so an
      // exact-visibility test would flap isFollowing to false on nearly
      // every new line during a live tail -- disabling the store's eviction
      // cap and popping a "jump to latest" button constantly. Anything
      // within 200px of the bottom still counts as following.
      { threshold: 0, rootMargin: '0px 0px 200px 0px' }
    )
    observer.observe(el)
  }

  watch(sentinel, attach)

  watch(itemCount, (next, prev) => {
    if (!isFollowing.value && next > prev) {
      newLineCount.value += next - prev
    }
  })

  function scrollToBottom() {
    sentinel.value?.scrollIntoView({ block: 'end' })
  }

  onBeforeUnmount(() => observer?.disconnect())

  return { sentinel, isFollowing, newLineCount, scrollToBottom }
}
