import { ref, watch, onBeforeUnmount } from 'vue'

// Tracks whether the viewport is scrolled down to a bottom sentinel element
// via IntersectionObserver, rather than polling scroll events. Consumers
// (JobDetailView.vue) use `isFollowing` both to gate the jobs store's
// live-log eviction cap (only trim resident lines while the tail is
// actually being watched) and to decide whether to auto-scroll new lines
// into view or surface a "N new lines" affordance instead of yanking a
// deliberately-scrolled-up reader back down.
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
      { threshold: 1.0 }
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
