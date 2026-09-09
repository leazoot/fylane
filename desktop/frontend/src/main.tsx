import React from 'react'
import {createRoot} from 'react-dom/client'
import './style.css'
import App from './App'
import {applyOS, detectOS} from './lib/platform'

// Before the first paint: the font stack is a platform choice, and text that
// reflows one frame in reads as a glitch.
applyOS(detectOS())

const container = document.getElementById('root')

const root = createRoot(container!)

root.render(
    <React.StrictMode>
        <App/>
    </React.StrictMode>
)
