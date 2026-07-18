<script setup>
import Sidebar from './components/Sidebar.vue'
import TokenGate from './components/TokenGate.vue'
import { useAuthStore } from './stores/auth'

const auth = useAuthStore()
</script>

<template>
  <TokenGate />
  <div v-if="auth.isAuthenticated" class="flex">
    <Sidebar />
    <main class="flex-1 p-6">
      <router-view />
    </main>
  </div>
</template>
