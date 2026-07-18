<template>
  <nav class="w-48 bg-gray-100 h-screen p-4 space-y-2">
    <router-link to="/clients" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Clients
    </router-link>
    <router-link to="/catalog" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Catalog
    </router-link>
    <router-link to="/policies" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Policies
    </router-link>
  </nav>
</template>
