// Which chain this page reports on: the node's, asked once, so a build
// pointed at another devnet names that devnet. A ZVM's chain id is 7340400
// plus its NoM chain's (zvm-core rollup), so one answer gives both. A ZVM
// devnet's, 7340469, until the node answers.
import {computed, ref} from 'vue'

export const rpc = `${(import.meta.env.VITE_ZVM_NODE as string).replace(/\/+$/, '')}/rpc`
export const zvmChain = ref(7340469)
export const nomChain = computed(() => zvmChain.value - 7340400)

// The gateway's host, which is how people name the devnet.
const gateway = ((import.meta.env.VITE_GATEWAY as string | undefined) || location.origin).replace(/\/+$/, '')
export const host = new URL(gateway, location.href).host

let asked = false
export async function askChain() {
  if (asked) return
  asked = true
  try {
    const r = await fetch(rpc, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({jsonrpc: '2.0', id: 1, method: 'eth_chainId', params: []})})
    const id = Number((await r.json()).result)
    if (id > 7340400) zvmChain.value = id
  } catch {
    /* keep 7340469 */
  }
}
