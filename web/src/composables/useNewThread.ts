import { useThreadsStore } from '@/stores/threads'
import { useToastStore } from '@/stores/toast'

// Shared "+ new" create flow, used by both the roster and the home board so
// the create + toast logic lives in one place. Returns the new thread id (or
// null on failure) so the caller can select it.
export function useNewThread() {
  const threads = useThreadsStore()
  const toast = useToastStore()

  async function newThread(backend?: string): Promise<string | null> {
    try {
      const t = await threads.create(backend)
      toast.show(
        backend === 'cc'
          ? 'claude code session started'
          : 'thread minted · frame resolves at attach',
      )
      return t.id
    } catch {
      toast.show('could not start that thread')
      return null
    }
  }

  return { newThread }
}
