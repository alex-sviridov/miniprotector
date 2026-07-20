<script setup>
import { ref } from 'vue'
import { useAuthStore } from '../stores/auth'
import BaseButton from './ui/BaseButton.vue'

const auth = useAuthStore()
const input = ref('')

function submit() {
  if (input.value.trim()) {
    auth.setToken(input.value.trim())
    input.value = ''
  }
}
</script>

<template>
  <div v-if="!auth.isAuthenticated" class="fixed inset-0 flex items-center justify-center bg-gray-900/80">
    <form @submit.prevent="submit" class="bg-white p-6 rounded shadow w-80 space-y-3">
      <h2 class="text-lg font-semibold">Enter API token</h2>
      <p v-if="auth.error" class="text-red-600 text-sm">{{ auth.error }}</p>
      <input
        v-model="input"
        type="password"
        placeholder="Bearer token"
        class="w-full border rounded px-2 py-1"
      />
      <BaseButton type="submit" variant="primary" class="w-full">Continue</BaseButton>
    </form>
  </div>
</template>
