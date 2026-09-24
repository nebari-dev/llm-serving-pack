import lockup from '@/assets/brand/Nebari-Logo-Horizontal-Lockup.svg'
import lockupWhite from '@/assets/brand/Nebari-Logo-Horizontal-Lockup-White-text.svg'
import { useTheme } from '@/hooks/theme-provider'

// The brand SVGs ship with a wide transparent margin (viewBox 1189.2 × 642.9,
// artwork at x 269–879, y 241–395). The assets are CC BY-NC-ND, so rather than
// editing the file we crop the margin in CSS and size by the artwork itself.
const VB = { w: 1189.2, h: 642.9 }
const ART = { x: 269, y: 241, w: 610, h: 154 }

export function NebariLogo({ height = 32 }: { height?: number }) {
  const { isDarkMode } = useTheme()
  const scale = height / ART.h
  return (
    <span className="relative block shrink-0 overflow-hidden" style={{ width: ART.w * scale, height }}>
      <img
        src={isDarkMode ? lockupWhite : lockup}
        alt="Nebari"
        className="absolute max-w-none"
        style={{ width: VB.w * scale, height: VB.h * scale, left: -ART.x * scale, top: -ART.y * scale }}
      />
    </span>
  )
}
