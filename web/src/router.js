import { createRouter, createWebHistory } from 'vue-router'
import HomeView from './views/HomeView.vue'
import ClientsListView from './views/ClientsListView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: HomeView },
    { path: '/clients', component: ClientsListView },
  ],
})
