// ApprovalBanner · with the rail collapsing when a thread has no sub-agents,
// the banner is the queue's one guaranteed surface. It shows the top approval
// and a "+N more waiting" hint for the overflow.

import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ApprovalBanner from './ApprovalBanner.vue'
import type { PendingApproval } from '@/stores/approvals'

const approval: PendingApproval = {
  requestId: 'req_8841',
  name: 'Bash',
  input: 'git push origin main',
  layerReason: 'fallback prompt',
}

describe('ApprovalBanner · queue hint', () => {
  it('shows no queue hint when this is the only pending approval', () => {
    const w = mount(ApprovalBanner, { props: { approval, queued: 0 } })
    expect(w.find('.a-queue').exists()).toBe(false)
    expect(w.text()).toContain('needs your call')
  })

  it('shows the overflow count when more approvals wait behind the top one', () => {
    const w = mount(ApprovalBanner, { props: { approval, queued: 2 } })
    expect(w.find('.a-queue').text()).toBe('+2 more waiting')
  })

  it('still resolves the top approval by request id', async () => {
    const w = mount(ApprovalBanner, { props: { approval, queued: 1 } })
    await w.find('button.deny').trigger('click')
    expect(w.emitted('resolve')).toEqual([['req_8841', 'deny']])
  })
})
